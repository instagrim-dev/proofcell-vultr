package githubapp

import (
	"context"
	"strings"
	"testing"

	"github.com/instagrim-dev/proofcell-vultr/internal/repair"
)

func TestQueuedClearInvalidatesIntentEvenAfterSameChoiceReselected(t *testing.T) {
	e, f, p := deliverySetup(t)
	sf := &statusDeliveryFake{deliveryFake: f}
	e.api = sf
	if err := selectOutcome(e, f, p, "keep"); err != nil {
		t.Fatal(err)
	}
	if err := confirmIntent(e, p); err != nil {
		t.Fatal(err)
	}
	ev := event("")
	ev.Kind = "issue_comment"
	ev.Action = "edited"
	ev.Comment.ID = p.DecisionComment
	ev.Comment.Body = p.DecisionBody
	// GitHub has already returned to the selected body before the queued clear is handled.
	if err := e.process(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if p.IntentConfirmedBy != "" || f.reports[p.IntentCheckID].Conclusion == "success" {
		t.Fatal("queued clear retained confirmation")
	}
}

func TestClearUpdatesVisibleIssueStatus(t *testing.T) {
	e, f, p := deliverySetup(t)
	sf := &statusDeliveryFake{deliveryFake: f}
	e.api = sf
	if err := selectOutcome(e, f, p, "keep"); err != nil {
		t.Fatal(err)
	}
	if err := confirmIntent(e, p); err != nil {
		t.Fatal(err)
	}
	ev := event("")
	ev.Kind = "issue_comment"
	ev.Action = "edited"
	ev.Comment.ID = p.DecisionComment
	ev.Comment.Body = p.DecisionBody
	f.bodies[p.DecisionComment] = p.DecisionBody
	if err := e.process(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.bodies[p.StatusCommentID], "No outcome selected") {
		t.Fatal(f.bodies[p.StatusCommentID])
	}
}

func TestUnknownSandboxSuiteCannotGetGreenVerification(t *testing.T) {
	e, f, p := deliverySetup(t)
	if err := selectOutcome(e, f, p, "keep"); err != nil {
		t.Fatal(err)
	}
	src := []byte("candidate")
	p.Files["access.go"] = src
	p.Generated = &repair.Result{Source: src, SourceSHA256: hash(src), Evidence: repair.Evidence{TestsSHA256: strings.Repeat("b", 64), CanaryPreserved: true, CleanupConfirmed: true}}
	if err := e.ensureDeliveryChecks(context.Background(), 1, "a/b", 2, p); err == nil {
		t.Fatal("unknown suite accepted")
	}
	if f.reports[p.CheckID].Conclusion != "failure" {
		t.Fatal("previous green check was not revoked")
	}
}

func TestExactProrationVerification(t *testing.T) {
	for _, tc := range []struct {
		json string
		ok   bool
	}{
		{`{"paid_cents":3000,"period_days":30,"unused_days":18,"proposed_refund_cents":1800,"refund_status":"proposed"}`, true},
		{`{"paid_cents":3000,"period_days":30,"unused_days":18,"proposed_refund_cents":1801,"refund_status":"proposed"}`, false},
		{`{"paid_cents":1,"period_days":3,"unused_days":1,"proposed_refund_cents":0,"refund_status":"proposed"}`, false},
	} {
		if err := verifyExactProration([]byte(tc.json)); (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.json, err)
		}
	}
}
