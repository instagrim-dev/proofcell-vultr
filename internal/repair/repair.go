// Package repair connects a bounded model-generated change to an independent sandbox.
package repair

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"strings"
	"time"
)

type Evidence struct {
	TestsSHA256      string `json:"tests_sha256"`
	Output           string `json:"output"`
	CanaryPreserved  bool   `json:"canary_preserved"`
	CleanupConfirmed bool   `json:"cleanup_confirmed"`
}
type Result struct {
	Source       []byte   `json:"source"`
	SourceSHA256 string   `json:"source_sha256"`
	Model        string   `json:"model"`
	RequestID    string   `json:"request_id"`
	Evidence     Evidence `json:"evidence"`
}
type Sandbox interface {
	Verify(context.Context, string, []byte) (Evidence, error)
}
type Service struct {
	Endpoint, Key, Model string
	Sandbox              Sandbox
	HTTP                 *http.Client
}

func digest(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func (s *Service) Repair(ctx context.Context, choice string, previous []byte) (Result, error) {
	if choice != "keep" && choice != "end" {
		return Result{}, fmt.Errorf("unsupported decision")
	}
	if s.Key == "" || s.Model == "" || s.Sandbox == nil {
		return Result{}, fmt.Errorf("repair worker not configured")
	}
	intent := "return false because cancellation ends access immediately"
	if choice == "keep" {
		intent = "return true strictly before the paid-until boundary, and false at or after it"
	}
	prompt := fmt.Sprintf("Owner decision: %s. Generate exactly one Go function: func HasAccess(paidUntil, now int64) bool. Use only the two input values and boolean/comparison expressions. Return only the function, no imports, no markdown. Existing code:\n%s", intent, string(previous))
	data, _ := json.Marshal(map[string]any{"model": s.Model, "messages": []map[string]string{{"role": "system", "content": "You are a Go programmer. Output only the requested Go function."}, {"role": "user", "content": prompt}}, "temperature": 0, "max_tokens": 512})
	req, err := http.NewRequestWithContext(ctx, "POST", s.Endpoint, bytes.NewReader(data))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Key)
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 75 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Result{}, fmt.Errorf("Vultr inference returned HTTP %d", resp.StatusCode)
	}
	var out struct {
		ID, Model string
		Choices   []struct{ Message struct{ Content string } }
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return Result{}, err
	}
	if out.ID == "" || len(out.Choices) == 0 {
		return Result{}, fmt.Errorf("inference response missing candidate")
	}
	source, err := candidate(out.Choices[0].Message.Content)
	if err != nil {
		return Result{}, err
	}
	evidence, err := s.Sandbox.Verify(ctx, choice, source)
	if err != nil {
		return Result{}, err
	}
	if !evidence.CanaryPreserved || !evidence.CleanupConfirmed {
		return Result{}, fmt.Errorf("sandbox containment verification failed")
	}
	return Result{Source: source, SourceSHA256: digest(source), Model: out.Model, RequestID: out.ID, Evidence: evidence}, nil
}
func candidate(raw string) ([]byte, error) {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "```go\n") && strings.HasSuffix(s, "```") {
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(s, "```go\n"), "```"))
	}
	if len(s) > 4096 {
		return nil, fmt.Errorf("candidate too large")
	}
	if !strings.HasPrefix(s, "package ") {
		s = "package subscription\n\n" + s
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "access.go", s, 0)
	if err != nil || f.Name.Name != "subscription" || len(f.Decls) != 1 {
		return nil, fmt.Errorf("candidate must be a single subscription function")
	}
	fn, ok := f.Decls[0].(*ast.FuncDecl)
	if !ok || fn.Recv != nil || fn.Name.Name != "HasAccess" || fn.Type.TypeParams != nil || fn.Type.Params == nil || fn.Type.Results == nil || fn.Body == nil || fn.Type.Params.NumFields() != 2 || fn.Type.Results.NumFields() != 1 || len(fn.Body.List) != 1 {
		return nil, fmt.Errorf("invalid function shape")
	}
	var signature bytes.Buffer
	_ = format.Node(&signature, fset, fn.Type)
	if signature.String() != "func(paidUntil, now int64) bool" {
		return nil, fmt.Errorf("invalid function signature")
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 || !safeExpr(ret.Results[0]) {
		return nil, fmt.Errorf("unsupported expression")
	}
	var buf bytes.Buffer
	if err = format.Node(&buf, fset, f); err != nil {
		return nil, err
	}
	return append(buf.Bytes(), '\n'), nil
}
func safeExpr(x ast.Expr) bool {
	switch v := x.(type) {
	case *ast.Ident:
		return v.Name == "now" || v.Name == "paidUntil" || v.Name == "true" || v.Name == "false"
	case *ast.ParenExpr:
		return safeExpr(v.X)
	case *ast.UnaryExpr:
		return v.Op == token.NOT && safeExpr(v.X)
	case *ast.BinaryExpr:
		switch v.Op {
		case token.LSS, token.GTR, token.LEQ, token.GEQ, token.EQL, token.NEQ, token.LAND, token.LOR:
			return safeExpr(v.X) && safeExpr(v.Y)
		}
	}
	return false
}
