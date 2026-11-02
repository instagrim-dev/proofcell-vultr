package githubapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

type nativeFake struct {
	*fakeAPI
	next   int64
	checks int
	head   string
}

func (f *nativeFake) ControlComment(_ context.Context, _ int64, _ string, _ int, body string) (int64, error) {
	f.next++
	f.comments = append(f.comments, body)
	return f.next, nil
}
func (f *nativeFake) PRHead(context.Context, int64, string, string) (string, error) {
	return f.head, nil
}
func (f *nativeFake) CreateDecisionCheck(context.Context, int64, string, string, string) (int64, error) {
	f.checks++
	return 77, nil
}

func TestNativeDecisionAndCheckActions(t *testing.T) {
	ctx := context.Background()
	f := &nativeFake{fakeAPI: &fakeAPI{title: "cancel", base: "abc", allowed: true}, head: "head-sha"}
	e, _ := Open(t.TempDir(), f)
	start := event("")
	start.Kind = "issues"
	start.Action = "labeled"
	start.Label.Name = "trajectory"
	if err := e.process(ctx, start); err != nil {
		t.Fatal(err)
	}
	p := e.data.Plans["1/a/b/2"]
	if p.DecisionComment == 0 || !strings.Contains(f.comments[len(f.comments)-1], "- [ ] Keep access") {
		t.Fatal("missing task list")
	}
	click := event("")
	click.Kind = "issue_comment"
	click.Action = "edited"
	click.Comment.ID = p.DecisionComment
	click.Comment.Body = strings.Replace(f.comments[len(f.comments)-1], "- [ ] Keep access", "- [x] Keep access", 1)
	if err := e.process(ctx, click); err != nil {
		t.Fatal(err)
	}
	if p.Choice != "keep" || p.PublishComment == 0 {
		t.Fatal("choice did not produce preview")
	}
	click.Comment.ID = p.PublishComment
	click.Comment.Body = strings.Replace(f.comments[len(f.comments)-1], "- [ ] Open draft pull request", "- [x] Open draft pull request", 1)
	if err := e.process(ctx, click); err != nil {
		t.Fatal(err)
	}
	if f.publications != 1 || p.CheckID != 77 || f.checks != 1 {
		t.Fatalf("publication/check missing: %#v", p)
	}
	action := event("")
	action.Kind = "check_run"
	action.Action = "requested_action"
	action.CheckRun.ID = 77
	action.CheckRun.HeadSHA = "head-sha"
	action.RequestedAction.Identifier = "show_decision"
	if err := e.process(ctx, action); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.comments[len(f.comments)-1], p.PatchHash) {
		t.Fatal("decision receipt missing")
	}
	f.allowed = false
	if err := e.process(ctx, action); err == nil {
		t.Fatal("unauthorized action accepted")
	}
}

func TestNativeRejectsEditedOrForeignControl(t *testing.T) {
	ctx := context.Background()
	f := &nativeFake{fakeAPI: &fakeAPI{title: "cancel", base: "abc", allowed: true}, head: "head"}
	e, _ := Open(t.TempDir(), f)
	start := event("")
	start.Kind = "issues"
	if err := e.process(ctx, start); err != nil {
		t.Fatal(err)
	}
	p := e.data.Plans["1/a/b/2"]
	click := event("")
	click.Kind = "issue_comment"
	click.Action = "edited"
	click.Comment.ID = p.DecisionComment + 5
	click.Comment.Body = strings.Replace(p.DecisionBody, "[ ]", "[x]", 1)
	if err := e.process(ctx, click); err != nil {
		t.Fatal(err)
	}
	if p.Choice != "" {
		t.Fatal("foreign control accepted")
	}
	click.Comment.ID = p.DecisionComment
	click.Comment.Body += "\nextra text"
	if err := e.process(ctx, click); err != nil {
		t.Fatal(err)
	}
	if p.Choice != "" {
		t.Fatal("tampered control accepted")
	}
}

func TestNativeRevalidateAndChangedHead(t *testing.T) {
	ctx := context.Background()
	f := &nativeFake{fakeAPI: &fakeAPI{title: "cancel", base: "abc", allowed: true}, head: "head"}
	e, _ := Open(t.TempDir(), f)
	start := event("")
	start.Kind = "issues"
	if err := e.process(ctx, start); err != nil {
		t.Fatal(err)
	}
	p := e.data.Plans["1/a/b/2"]
	click := event("")
	click.Kind = "issue_comment"
	click.Action = "edited"
	click.Comment.ID = p.DecisionComment
	click.Comment.Body = strings.Replace(p.DecisionBody, "[ ]", "[x]", 1)
	if err := e.process(ctx, click); err != nil {
		t.Fatal(err)
	}
	click.Comment.ID = p.PublishComment
	click.Comment.Body = strings.Replace(p.PublishBody, "[ ]", "[x]", 1)
	if err := e.process(ctx, click); err != nil {
		t.Fatal(err)
	}
	action := event("")
	action.Kind = "check_run"
	action.CheckRun.ID = p.CheckID
	action.CheckRun.HeadSHA = p.CheckHead
	action.RequestedAction.Identifier = "revalidate"
	if err := e.process(ctx, action); err != nil {
		t.Fatal(err)
	}
	if f.checks != 2 {
		t.Fatal("validation did not run")
	}
	f.head = "different"
	if err := e.process(ctx, action); err == nil {
		t.Fatal("changed PR head accepted")
	}
}

func TestNativeSignedWebhookFlow(t *testing.T) {
	f := &nativeFake{fakeAPI: &fakeAPI{title: "cancel", base: "abc", allowed: true}, head: "head"}
	e, _ := Open(t.TempDir(), f)
	send := func(kind, id string, ev Event) {
		t.Helper()
		b, _ := json.Marshal(ev)
		mac := hmac.New(sha256.New, []byte("secret"))
		mac.Write(b)
		req := httptest.NewRequest("POST", "/webhook", strings.NewReader(string(b)))
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		req.Header.Set("X-GitHub-Event", kind)
		req.Header.Set("X-GitHub-Delivery", id)
		w := httptest.NewRecorder()
		e.Handler("secret").ServeHTTP(w, req)
		if w.Code != 202 {
			t.Fatalf("webhook: %d", w.Code)
		}
		e.drain(context.Background())
	}
	start := event("")
	start.Action = "labeled"
	start.Label.Name = "trajectory"
	send("issues", "one", start)
	p := e.data.Plans["1/a/b/2"]
	if p == nil {
		t.Fatal("label did not start plan")
	}
	click := event("")
	click.Action = "edited"
	click.Comment.ID = p.DecisionComment
	click.Comment.Body = strings.Replace(p.DecisionBody, "[ ]", "[x]", 1)
	send("issue_comment", "two", click)
	if p.Choice != "keep" {
		t.Fatal("checkbox edit did not select")
	}
	click.Comment.ID = p.PublishComment
	click.Comment.Body = strings.Replace(p.PublishBody, "[ ]", "[x]", 1)
	send("issue_comment", "three", click)
	if f.publications != 1 || p.CheckID == 0 {
		t.Fatal("checkbox edit did not publish and create check")
	}
	action := event("")
	action.Action = "requested_action"
	action.Issue.Number = 0
	action.CheckRun.ID = p.CheckID
	action.CheckRun.HeadSHA = p.CheckHead
	action.RequestedAction.Identifier = "show_decision"
	send("check_run", "four", action)
	if !strings.Contains(f.comments[len(f.comments)-1], "Decision receipt") {
		t.Fatal("check action did not reach issue")
	}
}
