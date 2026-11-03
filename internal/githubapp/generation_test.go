package githubapp

import (
	"context"
	"fmt"
	"github.com/instagrim-dev/proofcell-vultr/internal/repair"
	"strings"
	"testing"
)

type generatedFake struct{ fail bool }

func (f generatedFake) Repair(_ context.Context, _ string, _ []byte) (repair.Result, error) {
	if f.fail {
		return repair.Result{}, fmt.Errorf("verification failed")
	}
	src := []byte("package subscription\nfunc HasAccess(paidUntil, now int64) bool { return now < paidUntil }\n")
	return repair.Result{Source: src, SourceSHA256: hash(src), Model: "test", RequestID: "req-1", Evidence: repair.Evidence{TestsSHA256: strings.Repeat("b", 64), CanaryPreserved: true, CleanupConfirmed: true}}, nil
}

type generationAPI struct{ *nativeFake }

func (f *generationAPI) File(ctx context.Context, i int64, repo, p, ref string) ([]byte, error) {
	if p == "access.go" {
		return []byte("old"), nil
	}
	return f.nativeFake.File(ctx, i, repo, p, ref)
}
func TestDecisionPublishesModelCandidateOnlyAfterVerification(t *testing.T) {
	ctx := context.Background()
	f := &generationAPI{&nativeFake{fakeAPI: &fakeAPI{title: "cancel", base: "abc", allowed: true}, head: "head"}}
	e, _ := Open(t.TempDir(), f)
	if err := e.process(ctx, event("/trajectory plan")); err != nil {
		t.Fatal(err)
	}
	p := e.data.Plans["1/a/b/2"]
	p.Config.Generation = "vultr-go-access-v1"
	p.Config.Choices[0].Changes = append(p.Config.Choices[0].Changes, Change{Path: "access.go", Before: hash([]byte("old")), Content: "authored"})
	e.Repairer = generatedFake{fail: true}
	if err := e.process(ctx, event("/trajectory choose "+p.ID+" keep")); err == nil {
		t.Fatal("unverified candidate accepted")
	}
	e.Repairer = generatedFake{}
	if err := e.process(ctx, event("/trajectory choose "+p.ID+" keep")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(p.Files["access.go"]), "now < paidUntil") || p.Generated.RequestID != "req-1" || !strings.Contains(f.comments[len(f.comments)-1], "Sandbox boundary tests passed") {
		t.Fatal("verified generated candidate/receipt missing")
	}
	if err := e.process(ctx, event("/trajectory publish "+p.ID+" "+p.PatchHash[:12])); err != nil {
		t.Fatal(err)
	}
	if p.PR == "" || f.publications != 1 || string(f.headFiles["access.go"]) != string(p.Generated.Source) {
		t.Fatal("published bytes did not match verified candidate")
	}
}
