package githubapp

import (
	"context"
	"encoding/json"
	"github.com/instagrim-dev/proofcell-vultr/internal/repair"
	"strings"
	"testing"
)

type rerunWorker struct {
	called   int
	evidence repair.Evidence
}

func (w *rerunWorker) Repair(context.Context, string, []byte) (repair.Result, error) {
	panic("rerun must not generate")
}
func (w *rerunWorker) Verify(_ context.Context, _ string, _ []byte) (repair.Evidence, error) {
	w.called++
	return w.evidence, nil
}
func TestRerequestRunsSandboxOnExistingCandidate(t *testing.T) {
	e, f, p := deliverySetup(t)
	if err := selectOutcome(e, f, p, "keep"); err != nil {
		t.Fatal(err)
	}
	src := []byte("package subscription\nfunc HasAccess(paidUntil,now int64) bool { return now<paidUntil }\n")
	p.Files["access.go"] = src
	encoded, _ := json.Marshal(p.Files)
	p.PatchHash = hash(encoded)
	f.headFiles = p.Files
	out := "--- PASS: TestBoundary/before_expiry \n--- PASS: TestBoundary/at_expiry \n--- PASS: TestBoundary/after_expiry \n--- PASS: TestBoundary/different_expiry \n"
	evd := repair.Evidence{TestsSHA256: repair.BoundaryTestsSHA256("keep"), Output: out, CanaryPreserved: true, CleanupConfirmed: true}
	p.Generated = &repair.Result{Source: src, SourceSHA256: hash(src), Evidence: evd}
	w := &rerunWorker{evidence: evd}
	e.Repairer = w
	ev := event("")
	ev.Kind = "check_run"
	ev.Action = "rerequested"
	ev.CheckRun.ID = p.CheckID
	ev.CheckRun.HeadSHA = p.CheckHead
	if err := e.process(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if w.called != 1 {
		t.Fatalf("sandbox runs %d", w.called)
	}
	if !strings.Contains(f.reports[p.CheckID].Text, "Sandbox re-run") {
		t.Fatal("rerun not identified")
	}
}
func TestBoundaryAnnotationReferencesActualFunction(t *testing.T) {
	p := &Plan{Choice: "keep", Files: map[string][]byte{"access.go": []byte("package subscription\n\nfunc HasAccess(paidUntil, now int64) bool { return now < paidUntil }\n")}, Generated: &repair.Result{}}
	a := proofAnnotations(p)
	if len(a) != 1 || a[0].Path != "access.go" || a[0].StartLine != 3 || !strings.Contains(a[0].Message, "tested") {
		t.Fatalf("%+v", a)
	}
}

func TestSuiteRerequestRoutesToVerification(t *testing.T) {
	e, f, p := deliverySetup(t)
	if err := selectOutcome(e, f, p, "keep"); err != nil {
		t.Fatal(err)
	}
	src := []byte("package subscription\nfunc HasAccess(paidUntil,now int64) bool { return now<paidUntil }\n")
	p.Files["access.go"] = src
	encoded, _ := json.Marshal(p.Files)
	p.PatchHash = hash(encoded)
	f.headFiles = p.Files
	out := "--- PASS: TestBoundary/before_expiry \n--- PASS: TestBoundary/at_expiry \n--- PASS: TestBoundary/after_expiry \n--- PASS: TestBoundary/different_expiry \n"
	evidence := repair.Evidence{TestsSHA256: repair.BoundaryTestsSHA256("keep"), Output: out, CanaryPreserved: true, CleanupConfirmed: true}
	p.Generated = &repair.Result{Source: src, SourceSHA256: hash(src), Evidence: evidence}
	worker := &rerunWorker{evidence: evidence}
	e.Repairer = worker
	ev := event("")
	ev.Kind = "check_suite"
	ev.Action = "rerequested"
	ev.CheckSuite.HeadSHA = p.CheckHead
	if err := e.process(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if worker.called != 1 {
		t.Fatal("suite re-run did not run sandbox")
	}
}
