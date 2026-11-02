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

type fakeAPI struct {
	title, body, base string
	comments          []string
	publications      int
	allowed           bool
}

func (f *fakeAPI) Repository(context.Context, int64, string) (string, string, error) {
	return "main", f.base, nil
}
func (f *fakeAPI) Issue(context.Context, int64, string, int) (string, string, error) {
	return f.title, f.body, nil
}
func (f *fakeAPI) CanWrite(context.Context, int64, string, string) (bool, error) {
	return f.allowed, nil
}
func (f *fakeAPI) File(_ context.Context, _ int64, _ string, p, _ string) ([]byte, error) {
	if p != ".trajectory.json" {
		return []byte(`{"access":"ended"}`), nil
	}
	cfg := Config{Version: 1, Question: "Keep paid access?", Choices: []Choice{{ID: "keep", Title: "Keep access", Changes: []Change{{Path: "policy.json", Before: hash([]byte(`{"access":"ended"}`)), Content: `{"access":"paid"}`}}, Checks: []Check{{Path: "policy.json", Key: "access", Equals: "paid"}}}, {ID: "end", Title: "End now", Changes: []Change{{Path: "policy.json", Before: hash([]byte(`{"access":"ended"}`)), Content: `{"access":"ended"}`}}, Checks: []Check{{Path: "policy.json", Key: "access", Equals: "ended"}}}}}
	b, _ := json.Marshal(cfg)
	return b, nil
}
func (f *fakeAPI) Comment(_ context.Context, _ int64, _ string, _ int, b string) error {
	f.comments = append(f.comments, b)
	return nil
}
func (f *fakeAPI) Publish(_ context.Context, _ int64, _, _, _, branch, _, _ string, files map[string][]byte) (string, error) {
	if branch == "main" {
		panic("default write")
	}
	f.publications++
	return "https://github.com/a/b/pull/1", nil
}
func event(command string) Event {
	var e Event
	e.Action = "created"
	e.Installation.ID = 1
	e.Repository.FullName = "a/b"
	e.Issue.Number = 2
	e.Sender.Login = "owner"
	e.Comment.Body = command
	return e
}
func TestDecisionPatchRevisionAndPublication(t *testing.T) {
	ctx := context.Background()
	f := &fakeAPI{title: "cancel", base: "abc", allowed: true}
	e, _ := Open(t.TempDir(), f)
	if err := e.process(ctx, event("/trajectory plan")); err != nil {
		t.Fatal(err)
	}
	p := e.data.Plans["1/a/b/2"]
	if err := e.process(ctx, event("/trajectory choose "+p.ID+" keep")); err != nil {
		t.Fatal(err)
	}
	old := p.PatchHash
	if string(p.Files["policy.json"]) != `{"access":"paid"}` {
		t.Fatal("wrong transformation")
	}
	if err := e.process(ctx, event("/trajectory choose "+p.ID+" end")); err != nil {
		t.Fatal(err)
	}
	if err := e.process(ctx, event("/trajectory publish "+p.ID+" "+old[:12])); err == nil {
		t.Fatal("stale patch accepted")
	}
	cmd := "/trajectory publish " + p.ID + " " + p.PatchHash[:12]
	if err := e.process(ctx, event(cmd)); err != nil {
		t.Fatal(err)
	}
	if err := e.process(ctx, event(cmd)); err != nil {
		t.Fatal(err)
	}
	if f.publications != 1 {
		t.Fatal("duplicate publish")
	}
	f.body = "edited"
	if err := e.process(ctx, event(cmd)); err == nil {
		t.Fatal("edited issue accepted")
	}
	f.body = ""
	f.base = "new"
	if err := e.process(ctx, event(cmd)); err == nil {
		t.Fatal("stale base accepted")
	}
}
func TestWebhookSignatureDurableReplayAndPermission(t *testing.T) {
	f := &fakeAPI{title: "cancel", base: "abc", allowed: true}
	dir := t.TempDir()
	e, _ := Open(dir, f)
	b, _ := json.Marshal(event("/trajectory plan"))
	send := func(sig string) int {
		r := httptest.NewRequest("POST", "/webhook", strings.NewReader(string(b)))
		r.Header.Set("X-Hub-Signature-256", sig)
		r.Header.Set("X-GitHub-Event", "issue_comment")
		r.Header.Set("X-GitHub-Delivery", "delivery-1")
		w := httptest.NewRecorder()
		e.Handler("secret").ServeHTTP(w, r)
		return w.Code
	}
	if send("sha256=bad") != 401 {
		t.Fatal("bad signature accepted")
	}
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write(b)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if send(sig) != 202 {
		t.Fatal("not queued")
	}
	reopened, err := Open(dir, f)
	if err != nil {
		t.Fatal(err)
	}
	reopened.drain(context.Background())
	if len(f.comments) != 1 {
		t.Fatal("persisted event missing")
	}
	if send(sig) != 202 {
		t.Fatal("duplicate not acknowledged")
	}
	reopened.drain(context.Background())
	if len(f.comments) != 1 {
		t.Fatal("duplicated delivery")
	}
	f.allowed = false
	if reopened.process(context.Background(), event("/trajectory plan")) == nil {
		t.Fatal("unauthorized command")
	}
}
func TestProtectedPaths(t *testing.T) {
	for _, p := range []string{"../a", "/a", ".github/workflows/run.yml", ".git/config", ".trajectory.json", "a/../b"} {
		if validPath(p) {
			t.Errorf("accepted %s", p)
		}
	}
}

func TestLateOlderChoiceCannotOverrideNewerDecision(t *testing.T) {
	ctx := context.Background()
	f := &fakeAPI{title: "cancel", base: "abc", allowed: true}
	e, _ := Open(t.TempDir(), f)
	plan := event("/trajectory plan")
	plan.Comment.ID = 10
	if err := e.process(ctx, plan); err != nil {
		t.Fatal(err)
	}
	p := e.data.Plans["1/a/b/2"]
	newer := event("/trajectory choose " + p.ID + " keep")
	newer.Comment.ID = 30
	if err := e.process(ctx, newer); err != nil {
		t.Fatal(err)
	}
	older := event("/trajectory choose " + p.ID + " end")
	older.Comment.ID = 20
	if err := e.process(ctx, older); err != nil {
		t.Fatal(err)
	}
	if p.Choice != "keep" {
		t.Fatal("old delivery overrode new decision")
	}
	plan.Comment.ID = 40
	if err := e.process(ctx, plan); err != nil {
		t.Fatal(err)
	}
	if e.data.Plans["1/a/b/2"].Choice != "keep" {
		t.Fatal("same plan reset selection")
	}
}
