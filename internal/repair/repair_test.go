package repair

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeSandbox struct {
	saw  string
	pass bool
}

func (f *fakeSandbox) Verify(_ context.Context, choice string, candidate []byte) (Evidence, error) {
	f.saw = string(candidate)
	if !f.pass {
		return Evidence{}, fmt.Errorf("boundary tests failed")
	}
	return Evidence{TestsSHA256: strings.Repeat("b", 64), Output: "PASS", CanaryPreserved: true, CleanupConfirmed: true}, nil
}
func TestRepairUsesModelCandidateAndIndependentSandbox(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("missing authentication")
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["max_tokens"].(float64) < 2048 {
			t.Error("reasoning model needs enough output budget to return a candidate")
		}
		fmt.Fprint(w, `{"id":"call-1","model":"test-model","choices":[{"message":{"content":"func HasAccess(paidUntil, now int64) bool { return now < paidUntil }"}}]}`)
	}))
	defer s.Close()
	sandbox := &fakeSandbox{pass: true}
	svc := Service{Endpoint: s.URL, Key: "token", Model: "test-model", Sandbox: sandbox}
	got, err := svc.Repair(context.Background(), "keep", []byte("package subscription\nfunc HasAccess(paidUntil, now int64) bool { return false }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestID != "call-1" || !strings.Contains(sandbox.saw, "now < paidUntil") || string(got.Source) != sandbox.saw || got.SourceSHA256 == "" {
		t.Fatalf("candidate/receipt not bound: %+v", got)
	}
	sandbox.pass = false
	if _, err := svc.Repair(context.Background(), "keep", nil); err == nil {
		t.Fatal("accepted failed independent verification")
	}
}
func TestCandidateRejectsImportsAndSideEffects(t *testing.T) {
	for _, s := range []string{"import \"os\"\nfunc HasAccess(paidUntil, now int64) bool { return true }", "func HasAccess(paidUntil, now int64) bool { return now < paidUntil }; func init() {}", "func HasAccess(paidUntil, now int64) bool { panic(\"x\"); return true }"} {
		if _, err := candidate(s); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
}

func TestUnixTransportPreservesCandidateAndReceipt(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"call-2","model":"test-model","choices":[{"message":{"content":"func HasAccess(paidUntil, now int64) bool { return false }"}}]}`)
	}))
	defer s.Close()
	tmp, err := os.MkdirTemp("/tmp", "tr-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	socket := filepath.Join(tmp, "worker.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: Handler(&Service{Endpoint: s.URL, Key: "secret", Model: "test-model", Sandbox: &fakeSandbox{pass: true}})}
	go server.Serve(l)
	defer server.Close()
	got, err := (Client{Socket: socket}).Repair(context.Background(), "end", []byte("old"))
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestID != "call-2" || got.SourceSHA256 != digest(got.Source) {
		t.Fatal("transport did not preserve candidate identity")
	}
}
