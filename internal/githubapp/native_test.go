package githubapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

type statefulNative struct {
	*nativeFake
	bodies      map[int64]string
	failPreview bool
}

func (f *statefulNative) ControlComment(ctx context.Context, i int64, r string, n int, body string) (int64, error) {
	if f.failPreview && strings.Contains(body, "Open draft pull request") {
		return 0, fmt.Errorf("simulated preview failure")
	}
	id, err := f.nativeFake.ControlComment(ctx, i, r, n, body)
	if err == nil {
		f.bodies[id] = body
	}
	return id, err
}
func (f *statefulNative) CurrentComment(_ context.Context, _ int64, _ string, id int64) (string, error) {
	return f.bodies[id], nil
}

func TestApprovalCannotPublishDifferentPatchAfterPreviewFailure(t *testing.T) {
	ctx := context.Background()
	f := &statefulNative{nativeFake: &nativeFake{fakeAPI: &fakeAPI{title: "cancel", base: "abc", allowed: true}, head: "head"}, bodies: map[int64]string{}}
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
	click.Comment.Body = strings.Replace(p.DecisionBody, "- [ ] Keep access", "- [x] Keep access", 1)
	f.bodies[click.Comment.ID] = click.Comment.Body
	if err := e.process(ctx, click); err != nil {
		t.Fatal(err)
	}
	oldID, oldBody := p.PublishComment, p.PublishBody
	f.failPreview = true
	click.Comment.Body = strings.Replace(p.DecisionBody, "- [ ] End now", "- [x] End now", 1)
	f.bodies[click.Comment.ID] = click.Comment.Body
	if err := e.process(ctx, click); err == nil {
		t.Fatal("expected preview failure")
	}
	click.Comment.ID = oldID
	click.Comment.Body = strings.Replace(oldBody, "[ ]", "[x]", 1)
	f.bodies[oldID] = click.Comment.Body
	_ = e.process(ctx, click)
	if f.publications != 0 {
		t.Fatal("old approval published a different patch")
	}
}

func TestOlderDecisionEventCannotOverrideCurrentComment(t *testing.T) {
	ctx := context.Background()
	f := &statefulNative{nativeFake: &nativeFake{fakeAPI: &fakeAPI{title: "cancel", base: "abc", allowed: true}, head: "head"}, bodies: map[int64]string{}}
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
	latest := strings.Replace(p.DecisionBody, "- [ ] End now", "- [x] End now", 1)
	old := strings.Replace(p.DecisionBody, "- [ ] Keep access", "- [x] Keep access", 1)
	f.bodies[click.Comment.ID] = latest
	click.Comment.Body = latest
	if err := e.process(ctx, click); err != nil {
		t.Fatal(err)
	}
	click.Comment.Body = old
	if err := e.process(ctx, click); err != nil {
		t.Fatal(err)
	}
	if p.Choice != "end" {
		t.Fatalf("stale event overwrote current choice: %s", p.Choice)
	}
}

type reportingNative struct {
	*statefulNative
	issueNumbers []int
}

func (f *reportingNative) Comment(ctx context.Context, i int64, r string, n int, b string) error {
	f.issueNumbers = append(f.issueNumbers, n)
	return f.fakeAPI.Comment(ctx, i, r, n, b)
}
func TestFailedCheckActionReportsToAssociatedIssue(t *testing.T) {
	f := &reportingNative{statefulNative: &statefulNative{nativeFake: &nativeFake{fakeAPI: &fakeAPI{allowed: true}, head: "changed"}, bodies: map[int64]string{}}}
	e, _ := Open(t.TempDir(), f)
	e.data.Plans["1/a/b/2"] = &Plan{PR: "https://github.com/a/b/pull/1", CheckID: 77, CheckHead: "original"}
	ev := event("")
	ev.Action = "requested_action"
	ev.Issue.Number = 0
	ev.CheckRun.ID = 77
	ev.CheckRun.HeadSHA = "original"
	ev.RequestedAction.Identifier = "revalidate"
	b, _ := json.Marshal(ev)
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(b)
	req := httptest.NewRequest("POST", "/webhook", strings.NewReader(string(b)))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-GitHub-Event", "check_run")
	req.Header.Set("X-GitHub-Delivery", "failure")
	w := httptest.NewRecorder()
	e.Handler("secret").ServeHTTP(w, req)
	for i := 0; i < 3; i++ {
		e.drain(context.Background())
	}
	if len(f.issueNumbers) != 1 || f.issueNumbers[0] != 2 {
		t.Fatalf("failure reported to wrong issue: %v", f.issueNumbers)
	}
}

func TestApprovalRejectedWhenLiveDecisionChanged(t *testing.T) {
	ctx := context.Background()
	f := &statefulNative{nativeFake: &nativeFake{fakeAPI: &fakeAPI{title: "cancel", base: "abc", allowed: true}, head: "head"}, bodies: map[int64]string{}}
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
	click.Comment.Body = strings.Replace(p.DecisionBody, "- [ ] Keep access", "- [x] Keep access", 1)
	f.bodies[click.Comment.ID] = click.Comment.Body
	if err := e.process(ctx, click); err != nil {
		t.Fatal(err)
	}
	f.bodies[p.DecisionComment] = strings.Replace(p.DecisionBody, "- [ ] End now", "- [x] End now", 1)
	click.Comment.ID = p.PublishComment
	click.Comment.Body = strings.Replace(p.PublishBody, "[ ]", "[x]", 1)
	f.bodies[click.Comment.ID] = click.Comment.Body
	if err := e.process(ctx, click); err == nil {
		t.Fatal("changed live decision did not block approval")
	}
	if f.publications != 0 {
		t.Fatal("published stale approval")
	}
}
func TestMultipleCheckedChoicesInvalidateApprovalAndExplain(t *testing.T) {
	ctx := context.Background()
	f := &statefulNative{nativeFake: &nativeFake{fakeAPI: &fakeAPI{title: "cancel", base: "abc", allowed: true}, head: "head"}, bodies: map[int64]string{}}
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
	click.Comment.Body = strings.Replace(p.DecisionBody, "- [ ] Keep access", "- [x] Keep access", 1)
	f.bodies[click.Comment.ID] = click.Comment.Body
	if err := e.process(ctx, click); err != nil {
		t.Fatal(err)
	}
	click.Comment.Body = strings.Replace(click.Comment.Body, "- [ ] End now", "- [x] End now", 1)
	f.bodies[click.Comment.ID] = click.Comment.Body
	if err := e.process(ctx, click); err != nil {
		t.Fatal(err)
	}
	if p.PublishComment != 0 || !strings.Contains(f.comments[len(f.comments)-1], "clear the other checkbox") {
		t.Fatal("ambiguous selection not invalidated and explained")
	}
}

type changingNative struct {
	*statefulNative
	decisionID int64
	changed    string
}

func (f *changingNative) Publish(ctx context.Context, i int64, r, base, sha, branch, title, body string, files map[string][]byte) (string, error) {
	f.bodies[f.decisionID] = f.changed
	if err := checkPublicationGuard(ctx); err != nil {
		return "", err
	}
	return f.fakeAPI.Publish(ctx, i, r, base, sha, branch, title, body, files)
}
func TestDecisionChangedDuringPublicationBlocksWrite(t *testing.T) {
	ctx := context.Background()
	f := &changingNative{statefulNative: &statefulNative{nativeFake: &nativeFake{fakeAPI: &fakeAPI{title: "cancel", base: "abc", allowed: true}, head: "head"}, bodies: map[int64]string{}}}
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
	click.Comment.Body = strings.Replace(p.DecisionBody, "- [ ] Keep access", "- [x] Keep access", 1)
	f.bodies[click.Comment.ID] = click.Comment.Body
	if err := e.process(ctx, click); err != nil {
		t.Fatal(err)
	}
	f.decisionID = p.DecisionComment
	f.changed = strings.Replace(p.DecisionBody, "- [ ] End now", "- [x] End now", 1)
	click.Comment.ID = p.PublishComment
	click.Comment.Body = strings.Replace(p.PublishBody, "[ ]", "[x]", 1)
	f.bodies[click.Comment.ID] = click.Comment.Body
	if err := e.process(ctx, click); err == nil {
		t.Fatal("changed decision during publication accepted")
	}
	if f.publications != 0 {
		t.Fatal("publication occurred")
	}
}
