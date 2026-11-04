package githubapp

import (
	"context"
	"strings"
	"testing"

	"github.com/instagrim-dev/proofcell-vultr/internal/repair"
)

func TestDecisionCardKeepsCanonicalCheckboxesAndDescriptions(t *testing.T) {
	cfg := Config{Question: "What next?", Delivery: "on-decision", Choices: []Choice{{ID: "keep", Title: "Keep access", Description: "Would keep access."}, {ID: "end", Title: "End access", Description: "Would end access."}}}
	body := decisionComment("plan", cfg)
	for _, want := range []string{"- [ ] Keep access <!-- keep -->", "  Would keep access.", "clear the selected box first", "invalidates"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
	selected := strings.Replace(body, "- [ ] Keep access <!-- keep -->", "- [x] Keep access <!-- keep -->", 1)
	if selected == body {
		t.Fatal("checkbox line cannot be selected")
	}
}

func TestAccessDemoDefaultsAreScopedToGenerationMode(t *testing.T) {
	cfg := Config{Generation: "vultr-go-access-v1", Choices: []Choice{{ID: "keep", Title: "Keep"}, {ID: "refund", Title: "Refund"}}}
	body := decisionComment("plan", cfg)
	if !strings.Contains(body, "Would keep access until the paid period ends") || !strings.Contains(body, "proposed prorated refund") {
		t.Fatal(body)
	}
	cfg.Generation = ""
	if strings.Contains(decisionComment("plan", cfg), "paid period") {
		t.Fatal("unrelated configuration received access descriptions")
	}
}

func TestDescribedDecisionSelectionAndReselection(t *testing.T) {
	e, f, p := deliverySetup(t)
	for i := range p.Config.Choices {
		p.Config.Choices[i].Description = "Would change the selected policy."
	}
	p.DecisionBody = decisionComment(p.ID, p.Config)
	f.bodies[p.DecisionComment] = p.DecisionBody
	if err := selectOutcome(e, f, p, "keep"); err != nil {
		t.Fatal(err)
	}
	if err := confirmIntent(e, p); err != nil {
		t.Fatal(err)
	}
	old := p.IntentCheckID
	ev := event("")
	ev.Kind = "issue_comment"
	ev.Action = "edited"
	ev.Comment.ID = p.DecisionComment
	ev.Comment.Body = p.DecisionBody
	f.bodies[p.DecisionComment] = p.DecisionBody
	if err := e.process(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if f.reports[old].Conclusion != "cancelled" {
		t.Fatal("clearing choice did not invalidate intent")
	}
	if err := selectOutcome(e, f, p, "end"); err != nil {
		t.Fatal(err)
	}
	if p.Choice != "end" || f.publications != 2 {
		t.Fatal("new choice did not publish")
	}
}

func TestProofSummariesAreSpecificToVerifiedEvidence(t *testing.T) {
	out := "--- PASS: TestBoundary/before_expiry (0.00s)\n--- PASS: TestBoundary/at_expiry (0.00s)\n--- PASS: TestBoundary/after_expiry (0.00s)\n--- PASS: TestBoundary/different_expiry (0.00s)\n"
	for _, tc := range []struct{ choice, want string }{{"keep", "allowed just before expiry"}, {"refund", "denied just before expiry"}} {
		p := &Plan{Choice: tc.choice, CheckHead: "abcdef123456", Generated: &repair.Result{Evidence: repair.Evidence{TestsSHA256: repair.BoundaryTestsSHA256(tc.choice), Output: out}}}
		s := proofSummary(p)
		if !strings.Contains(s, tc.want) || !strings.Contains(s, "2/2") || !strings.Contains(s, "abcdef123456") {
			t.Fatalf("%s: %s", tc.choice, s)
		}
		p.Generated.Evidence.TestsSHA256 = "other"
		if strings.Contains(proofSummary(p), "2/2") {
			t.Fatal("unrecognized tests reported as boundary proof")
		}
	}
	p := &Plan{}
	if strings.Contains(proofSummary(p), "2/2") {
		t.Fatal("unverified run reported as boundary proof")
	}
}
