package githubapp

import (
	"context"
	"fmt"
	"github.com/instagrim-dev/proofcell-vultr/internal/repair"
	"strings"
	"testing"
)

func TestReviewRepeatedPlanDoesNotLeaveGreenIntentOnUncheckedControl(t *testing.T) {
	e, f, p := deliverySetup(t)
	if err := selectOutcome(e, f, p, "keep"); err != nil {
		t.Fatal(err)
	}
	if err := confirmIntent(e, p); err != nil {
		t.Fatal(err)
	}
	old := p.DecisionComment
	if err := e.process(context.Background(), event("/trajectory plan")); err != nil {
		t.Fatal(err)
	}
	t.Logf("new control=%d old=%d body=%q confirmed=%s check=%s", p.DecisionComment, old, f.bodies[p.DecisionComment], p.IntentConfirmedBy, f.reports[p.IntentCheckID].Conclusion)
	if p.DecisionComment != old && !strings.Contains(f.bodies[p.DecisionComment], "[x]") && f.reports[p.IntentCheckID].Conclusion == "success" {
		t.Fatal("green intent retained on a new unchecked canonical control")
	}
}

type reviewAlternatingRepair struct{ calls int }

func (r *reviewAlternatingRepair) Repair(context.Context, string, []byte) (repair.Result, error) {
	r.calls++
	src := []byte("package subscription\nfunc HasAccess(paidUntil,now int64)bool{return now<paidUntil}\n")
	if r.calls > 1 {
		src = []byte("package subscription\nfunc HasAccess(paidUntil,now int64)bool{return paidUntil>now}\n")
	}
	return repair.Result{Source: src, SourceSHA256: hash(src), Model: "test", RequestID: fmt.Sprint(r.calls), Evidence: repair.Evidence{TestsSHA256: repair.BoundaryTestsSHA256("keep"), Output: "--- PASS: TestBoundary/before_expiry \n--- PASS: TestBoundary/at_expiry \n--- PASS: TestBoundary/after_expiry \n--- PASS: TestBoundary/different_expiry \n", CanaryPreserved: true, CleanupConfirmed: true}}, nil
}

type reviewLostPublish struct {
	*deliveryFake
	branches []string
}

func (f *reviewLostPublish) File(ctx context.Context, i int64, repo, p, ref string) ([]byte, error) {
	if p == "access.go" {
		return []byte("old"), nil
	}
	return f.deliveryFake.File(ctx, i, repo, p, ref)
}
func (f *reviewLostPublish) Publish(ctx context.Context, i int64, repo, base, sha, branch, title, body string, files map[string][]byte) (string, error) {
	f.branches = append(f.branches, branch)
	if len(f.branches) == 1 {
		return "", fmt.Errorf("PR response lost after creation")
	}
	return f.deliveryFake.Publish(ctx, i, repo, base, sha, branch, title, body, files)
}
func TestReviewRetryKeepsPreviouslyVerifiedCandidate(t *testing.T) {
	e, f, p := deliverySetup(t)
	api := &reviewLostPublish{deliveryFake: f}
	e.api = api
	r := &reviewAlternatingRepair{}
	e.Repairer = r
	p.Config.Generation = "vultr-go-access-v1"
	p.Config.Choices[0].Changes = append(p.Config.Choices[0].Changes, Change{Path: "access.go", Before: hash([]byte("old")), Content: "authored"})
	if err := selectOutcome(e, f, p, "keep"); err == nil {
		t.Fatal("expected transient loss")
	}
	if err := selectOutcome(e, f, p, "keep"); err != nil {
		t.Fatal(err)
	}
	t.Logf("model calls=%d published branches=%v", r.calls, api.branches)
	if r.calls != 1 || api.branches[0] != api.branches[1] {
		t.Fatal("publication retry regenerated candidate and changed outcome branch")
	}
}
