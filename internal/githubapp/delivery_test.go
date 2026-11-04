package githubapp

import (
	"context"
	"strings"
	"testing"
)

func TestOnDecisionCreatesDraftAndChecksWithoutPublicationCheckbox(t *testing.T) {
	e, f, p := deliverySetup(t)
	if err := selectOutcome(e, f, p, "keep"); err != nil {
		t.Fatal(err)
	}
	if p.PR == "" || f.publications != 1 {
		t.Fatal("decision did not create its draft outcome")
	}

	for _, c := range f.comments {
		if strings.Contains(c, "Open draft pull request") || strings.Contains(c, "```diff") {
			t.Fatal("issue still contains publication control or patch dump")
		}
	}
}

type deliveryFake struct {
	*statefulNative
	reports map[int64]CheckReport
}

func (f *deliveryFake) PutDeliveryCheck(_ context.Context, _ int64, _ string, r CheckReport) (int64, error) {
	if r.ID == 0 {
		r.ID = int64(100 + len(f.reports))
	}
	f.reports[r.ID] = r
	return r.ID, nil
}
func deliverySetup(t *testing.T) (*Engine, *deliveryFake, *Plan) {
	t.Helper()
	f := &deliveryFake{statefulNative: &statefulNative{nativeFake: &nativeFake{fakeAPI: &fakeAPI{title: "Cancel renewal", body: "Preserve paid time", base: "abc", allowed: true}, head: "head"}, bodies: map[int64]string{}}, reports: map[int64]CheckReport{}}
	e, _ := Open(t.TempDir(), f)
	if err := e.process(context.Background(), event("/trajectory plan")); err != nil {
		t.Fatal(err)
	}
	p := e.data.Plans["1/a/b/2"]
	p.Config.Delivery = "on-decision"
	return e, f, p
}
func selectOutcome(e *Engine, f *deliveryFake, p *Plan, id string) error {
	var title string
	for _, c := range p.Config.Choices {
		if c.ID == id {
			title = c.Title
		}
	}
	ev := event("")
	ev.Kind = "issue_comment"
	ev.Action = "edited"
	ev.Comment.ID = p.DecisionComment
	ev.Comment.Body = strings.Replace(p.DecisionBody, "- [ ] "+title, "- [x] "+title, 1)
	f.bodies[ev.Comment.ID] = ev.Comment.Body
	return e.process(context.Background(), ev)
}
func confirmIntent(e *Engine, p *Plan) error {
	ev := event("")
	ev.Kind = "check_run"
	ev.Action = "requested_action"
	ev.CheckRun.ID = p.IntentCheckID
	ev.CheckRun.HeadSHA = p.CheckHead
	ev.RequestedAction.Identifier = "confirm_intent"
	return e.process(context.Background(), ev)
}
func TestIntentConfirmationBindsCurrentIssueDecisionAndPatch(t *testing.T) {
	e, f, p := deliverySetup(t)
	if err := selectOutcome(e, f, p, "keep"); err != nil {
		t.Fatal(err)
	}
	if p.CheckID == 0 || p.IntentCheckID == 0 || f.reports[p.IntentCheckID].Conclusion != "action_required" {
		t.Fatal("missing native proof/intent checks")
	}
	if err := confirmIntent(e, p); err != nil {
		t.Fatal(err)
	}
	if f.reports[p.IntentCheckID].Conclusion != "success" || p.IntentConfirmedBy != "owner" {
		t.Fatal("intent confirmation not recorded")
	}
	old := p.IntentCheckID
	if err := selectOutcome(e, f, p, "end"); err != nil {
		t.Fatal(err)
	}
	if f.publications != 2 || p.IntentCheckID == old || f.reports[old].Conclusion != "cancelled" || p.IntentConfirmedBy != "" {
		t.Fatal("redecision retained prior intent approval")
	}
}
func TestIntentRejectsChangedInputs(t *testing.T) {
	for _, change := range []string{"issue", "base", "head", "selection", "permission", "files"} {
		t.Run(change, func(t *testing.T) {
			e, f, p := deliverySetup(t)
			if err := selectOutcome(e, f, p, "keep"); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "issue":
				f.body = "different request"
			case "base":
				f.base = "new"
			case "head":
				f.head = "new"
			case "selection":
				f.bodies[p.DecisionComment] = p.DecisionBody
			case "permission":
				f.allowed = false
			case "files":
				f.headFiles = map[string][]byte{}
			}
			if err := confirmIntent(e, p); err == nil {
				t.Fatal("accepted changed " + change)
			}
			if f.reports[p.IntentCheckID].Conclusion == "success" {
				t.Fatal("stale intent remains green")
			}
		})
	}
}

func TestEditingIssueRevokesConfirmedIntent(t *testing.T) {
	e, f, p := deliverySetup(t)
	if err := selectOutcome(e, f, p, "keep"); err != nil {
		t.Fatal(err)
	}
	if err := confirmIntent(e, p); err != nil {
		t.Fatal(err)
	}
	f.body = "Now refund the unused time"
	ev := event("")
	ev.Kind = "issues"
	ev.Action = "edited"
	if err := e.process(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if f.reports[p.IntentCheckID].Conclusion != "cancelled" || p.IntentConfirmedBy != "" {
		t.Fatal("edited issue retained successful intent check")
	}
}

func TestNativeDeliveryActionsFitGitHubLimits(t *testing.T) {
	e, f, p := deliverySetup(t)
	if err := selectOutcome(e, f, p, "keep"); err != nil {
		t.Fatal(err)
	}
	for _, r := range f.reports {
		for _, a := range r.Actions {
			if len(a.Description) > 40 || len(a.Label) > 20 || len(a.Identifier) > 20 {
				t.Fatalf("invalid GitHub action: %+v", a)
			}
		}
	}
}
