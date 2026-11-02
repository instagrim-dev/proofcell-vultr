// Package githubapp drives repository-authored decision branches through GitHub issues.
package githubapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type API interface {
	Repository(context.Context, int64, string) (string, string, error)
	Issue(context.Context, int64, string, int) (string, string, error)
	CanWrite(context.Context, int64, string, string) (bool, error)
	File(context.Context, int64, string, string, string) ([]byte, error)
	Comment(context.Context, int64, string, int, string) error
	Publish(context.Context, int64, string, string, string, string, string, string, map[string][]byte) (string, error)
}
type Change struct {
	Path    string `json:"path"`
	Before  string `json:"before_sha256"`
	Content string `json:"content"`
}
type Check struct {
	Path   string `json:"path"`
	Key    string `json:"key"`
	Equals any    `json:"equals"`
}
type Choice struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Changes []Change `json:"changes"`
	Checks  []Check  `json:"checks"`
}
type Config struct {
	Version  int      `json:"version"`
	Question string   `json:"question"`
	Choices  []Choice `json:"choices"`
}
type Plan struct {
	DecisionComment, PublishComment, CheckID                          int64
	DecisionBody, PublishBody, CheckHead                              string
	Pending                                                           bool
	LastComment                                                       int64
	ID, IssueDigest, BaseBranch, BaseSHA, Choice, PatchHash, Diff, PR string
	Config                                                            Config
	Files                                                             map[string][]byte
	History                                                           []string
}
type Event struct {
	Kind  string `json:"_kind"`
	Label struct {
		Name string `json:"name"`
	} `json:"label"`
	CheckRun struct {
		ID      int64  `json:"id"`
		HeadSHA string `json:"head_sha"`
	} `json:"check_run"`
	RequestedAction struct {
		Identifier string `json:"identifier"`
	} `json:"requested_action"`
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Issue struct {
		Number      int             `json:"number"`
		PullRequest json.RawMessage `json:"pull_request"`
	} `json:"issue"`
	Comment struct {
		Body string `json:"body"`
		ID   int64  `json:"id"`
		User struct {
			Type string `json:"type"`
		} `json:"user"`
	} `json:"comment"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}
type Job struct {
	Event         Event
	Status, Error string
	Attempts      int
}
type disk struct {
	Plans map[string]*Plan
	Jobs  map[string]*Job
}
type Engine struct {
	mu   sync.Mutex
	dir  string
	api  API
	data disk
	wake chan struct{}
}

func hash(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func issueHash(title, body string) string {
	b, _ := json.Marshal([]string{title, body})
	return hash(b)
}
func Open(dir string, api API) (*Engine, error) {
	if api == nil {
		return nil, fmt.Errorf("GitHub API required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	e := &Engine{dir: dir, api: api, wake: make(chan struct{}, 1), data: disk{Plans: map[string]*Plan{}, Jobs: map[string]*Job{}}}
	b, err := os.ReadFile(path.Join(dir, "state.json"))
	if err == nil {
		if err = json.Unmarshal(b, &e.data); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if e.data.Plans == nil || e.data.Jobs == nil {
		return nil, fmt.Errorf("invalid persisted state")
	}
	for _, j := range e.data.Jobs {
		if j.Status == "processing" {
			j.Status = "queued"
		}
	}
	return e, nil
}
func (e *Engine) save() error {
	b, err := json.MarshalIndent(e.data, "", "  ")
	if err != nil {
		return err
	}
	p := path.Join(e.dir, "state.json")
	if err = os.WriteFile(p+".tmp", b, 0600); err != nil {
		return err
	}
	f, err := os.OpenFile(p+".tmp", os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	err = f.Sync()
	f.Close()
	if err != nil {
		return err
	}
	if err = os.Rename(p+".tmp", p); err != nil {
		return err
	}
	d, err := os.Open(e.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (e *Engine) Handler(secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST required", 405)
			return
		}
		if secret == "" {
			http.Error(w, "not configured", 503)
			return
		}
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "invalid body", 400)
			return
		}
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(b)
		sig, err := hex.DecodeString(strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256="))
		if err != nil || !strings.HasPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256=") || !hmac.Equal(sig, mac.Sum(nil)) {
			http.Error(w, "invalid signature", 401)
			return
		}
		if r.Header.Get("X-GitHub-Event") == "ping" {
			w.WriteHeader(200)
			return
		}
		var ev Event
		if err = json.Unmarshal(b, &ev); err != nil {
			http.Error(w, "invalid event", 400)
			return
		}
		ev.Kind = r.Header.Get("X-GitHub-Event")
		isIssue := ev.Kind == "issues" && ev.Action == "labeled" && ev.Label.Name == "trajectory"
		isComment := ev.Kind == "issue_comment" && ((ev.Action == "created" && ev.Comment.User.Type != "Bot" && strings.HasPrefix(strings.TrimSpace(ev.Comment.Body), "/trajectory ")) || ev.Action == "edited") && (len(ev.Issue.PullRequest) == 0 || string(ev.Issue.PullRequest) == "null")
		isCheck := ev.Kind == "check_run" && ev.Action == "requested_action"
		if !isIssue && !isComment && !isCheck {
			w.WriteHeader(202)
			return
		}
		id := r.Header.Get("X-GitHub-Delivery")
		if id == "" || len(id) > 200 || ev.Installation.ID <= 0 || (!isCheck && ev.Issue.Number <= 0) || (isCheck && ev.CheckRun.ID <= 0) || !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(ev.Repository.FullName) {
			http.Error(w, "missing event identity", 400)
			return
		}
		deliveries := path.Join(e.dir, "deliveries")
		if err = os.MkdirAll(deliveries, 0700); err != nil {
			http.Error(w, "persistence unavailable", 503)
			return
		}
		payload, _ := json.Marshal(Job{Event: ev, Status: "queued"})
		tmp, err := os.CreateTemp(deliveries, ".incoming-")
		if err != nil {
			http.Error(w, "persistence unavailable", 503)
			return
		}
		tempName := tmp.Name()
		defer os.Remove(tempName)
		if _, err = tmp.Write(payload); err == nil {
			err = tmp.Sync()
		}
		tmp.Close()
		if err == nil {
			err = os.Link(tempName, path.Join(deliveries, hash([]byte(id))+".json"))
		}
		if err != nil && !os.IsExist(err) {
			http.Error(w, "persistence unavailable", 503)
			return
		}
		parent, perr := os.Open(e.dir)
		if perr != nil {
			http.Error(w, "persistence unavailable", 503)
			return
		}
		perr = parent.Sync()
		parent.Close()
		if perr != nil {
			http.Error(w, "persistence unavailable", 503)
			return
		}
		d, err := os.Open(deliveries)
		if err != nil {
			http.Error(w, "persistence unavailable", 503)
			return
		}
		err = d.Sync()
		d.Close()
		if err != nil {
			http.Error(w, "persistence unavailable", 503)
			return
		}
		select {
		case e.wake <- struct{}{}:
		default:
		}

		w.WriteHeader(202)
	})
}

// Start recovers persisted deliveries and serializes state transitions. All GitHub calls are bounded by context.
func (e *Engine) Start(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		e.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-e.wake:
		case <-ticker.C:
		}
	}
}
func (e *Engine) drain(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entries, _ := os.ReadDir(path.Join(e.dir, "deliveries"))
	sort.SliceStable(entries, func(i, j int) bool {
		var a, b Job
		aa, _ := os.ReadFile(path.Join(e.dir, "deliveries", entries[i].Name()))
		bb, _ := os.ReadFile(path.Join(e.dir, "deliveries", entries[j].Name()))
		_ = json.Unmarshal(aa, &a)
		_ = json.Unmarshal(bb, &b)
		return a.Event.Comment.ID < b.Event.Comment.ID
	})
	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		file := path.Join(e.dir, "deliveries", entry.Name())
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var j Job
		if json.Unmarshal(data, &j) != nil {
			continue
		}
		if j.Status == "done" || j.Attempts >= 3 {
			continue
		}
		j.Attempts++
		call, cancel := context.WithTimeout(ctx, 90*time.Second)
		err = e.process(call, j.Event)
		cancel()
		if err != nil {
			j.Status = "failed"
			j.Error = err.Error()
			if j.Attempts == 3 {
				_ = e.api.Comment(ctx, j.Event.Installation.ID, j.Event.Repository.FullName, j.Event.Issue.Number, "Trajectory could not complete this command: "+j.Error+". Submit a new command after correcting the inputs or restoring service.")
			}
		} else {
			j.Status = "done"
			j.Error = ""
		}
		payload, _ := json.Marshal(j)
		if os.WriteFile(file+".tmp", payload, 0600) != nil {
			return
		}
		f, err := os.OpenFile(file+".tmp", os.O_RDWR, 0600)
		if err != nil {
			return
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return
		}
		if os.Rename(file+".tmp", file) != nil {
			return
		}
	}
}

func validPath(p string) bool {
	return p != "" && len(p) < 240 && path.Clean(p) == p && !strings.HasPrefix(p, "/") && !strings.Contains(p, "\\") && p != ".." && !strings.HasPrefix(p, "../") && !strings.HasPrefix(p, ".git") && p != ".trajectory.json"
}
func (e *Engine) process(ctx context.Context, ev Event) error {
	inst, repo, n := ev.Installation.ID, ev.Repository.FullName, ev.Issue.Number
	if ev.Kind == "check_run" {
		return e.processCheck(ctx, ev)
	}
	if ev.Kind == "issues" {
		ev.Comment.Body = "/trajectory plan"
		ev.Comment.ID = 0
	}
	if ev.Kind == "issue_comment" && ev.Action == "edited" {
		p := e.data.Plans[fmt.Sprintf("%d/%s/%d", inst, repo, n)]
		if p == nil {
			return nil
		}
		switch ev.Comment.ID {
		case p.DecisionComment:
			if p.DecisionComment == 0 || p.PR != "" {
				return nil
			}
			choice := ""
			for _, c := range p.Config.Choices {
				line := "- [ ] " + c.Title + " <!-- " + c.ID + " -->"
				if ev.Comment.Body == strings.Replace(p.DecisionBody, line, strings.Replace(line, "[ ]", "[x]", 1), 1) {
					choice = c.ID
					break
				}
			}
			if choice == "" {
				return nil
			}
			ev.Comment.Body = "/trajectory choose " + p.ID + " " + choice
		case p.PublishComment:
			if p.PublishComment == 0 || ev.Comment.Body != strings.Replace(p.PublishBody, "- [ ] Open draft pull request", "- [x] Open draft pull request", 1) {
				return nil
			}
			ev.Comment.Body = "/trajectory publish " + p.ID + " " + p.PatchHash[:12]
		default:
			return nil
		}
		ev.Comment.ID = 0
	}
	ok, err := e.api.CanWrite(ctx, inst, repo, ev.Sender.Login)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("command requires current repository write permission")
	}
	parts := strings.Fields(ev.Comment.Body)
	if len(parts) < 2 || parts[0] != "/trajectory" {
		return nil
	}
	title, body, err := e.api.Issue(ctx, inst, repo, n)
	if err != nil {
		return err
	}
	branch, base, err := e.api.Repository(ctx, inst, repo)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%d/%s/%d", inst, repo, n)
	p := e.data.Plans[key]
	if p != nil && ev.Comment.ID > 0 && ev.Comment.ID <= p.LastComment {
		return nil
	}
	switch parts[1] {
	case "plan":
		b, err := e.api.File(ctx, inst, repo, ".trajectory.json", base)
		if err != nil {
			return err
		}
		if len(b) > 128<<10 {
			return fmt.Errorf("config too large")
		}
		var cfg Config
		if err = json.Unmarshal(b, &cfg); err != nil {
			return err
		}
		if cfg.Version != 1 || cfg.Question == "" || len(cfg.Choices) < 2 || len(cfg.Choices) > 8 {
			return fmt.Errorf("expected version 1, question, and 2–8 choices")
		}
		ids := map[string]bool{}
		for _, c := range cfg.Choices {
			if !regexp.MustCompile(`^[a-z0-9-]{1,40}$`).MatchString(c.ID) || ids[c.ID] || c.Title == "" || strings.ContainsAny(c.Title, "\r\n<>") || len(c.Changes) == 0 || len(c.Changes) > 10 || len(c.Checks) == 0 {
				return fmt.Errorf("invalid choice")
			}
			ids[c.ID] = true
		}
		id := hash([]byte(issueHash(title, body) + base + hash(b)))[:12]
		if p == nil || p.ID != id {
			p = &Plan{ID: id, IssueDigest: issueHash(title, body), BaseBranch: branch, BaseSHA: base, Config: cfg}
		}
		if ev.Comment.ID > 0 {
			p.LastComment = ev.Comment.ID
		}
		e.data.Plans[key] = p
		if err = e.save(); err != nil {
			return err
		}
		msg := "### Trajectory decision `" + id + "`\n" + cfg.Question + "\n\nSelect one option:\n"
		for _, c := range cfg.Choices {
			msg += fmt.Sprintf("- [ ] %s <!-- %s -->\n", c.Title, c.ID)
		}
		if api, ok := e.api.(nativeAPI); ok {
			comment, err := api.ControlComment(ctx, inst, repo, n, msg)
			if err != nil {
				return err
			}
			p.DecisionComment, p.DecisionBody = comment, msg
			return e.save()
		}
		return e.api.Comment(ctx, inst, repo, n, msg)
	case "choose", "publish":
		if p == nil || len(parts) < 3 || parts[2] != p.ID {
			return fmt.Errorf("use the current plan ID")
		}
		if p.IssueDigest != issueHash(title, body) || p.BaseSHA != base {
			if parts[1] == "publish" && p.Pending {
				if recovery, ok := e.api.(interface {
					Published(context.Context, int64, string, string, string) (string, error)
				}); ok {
					b := fmt.Sprintf("trajectory/issue-%d-%s-%s", n, p.ID, p.PatchHash[:12])
					url, err := recovery.Published(ctx, inst, repo, p.BaseBranch, b)
					if err != nil {
						return err
					}
					if url != "" {
						p.PR = url
						p.Pending = false
						if err = e.save(); err != nil {
							return err
						}
						return e.api.Comment(ctx, inst, repo, n, "Recovered prior draft PR: "+url+". Inputs have since changed; this is the earlier decision artifact.")
					}
				}
			}

			return fmt.Errorf("issue inputs or base changed; run /trajectory plan again")
		}
	default:
		return fmt.Errorf("use plan, choose PLAN CHOICE, or publish PLAN PATCH")
	}
	if parts[1] == "choose" {
		if len(parts) != 4 {
			return fmt.Errorf("choose requires plan and choice IDs")
		}
		var selected *Choice
		for i := range p.Config.Choices {
			if p.Config.Choices[i].ID == parts[3] {
				selected = &p.Config.Choices[i]
			}
		}
		if selected == nil {
			return fmt.Errorf("unknown choice")
		}
		files := map[string][]byte{}
		diff := ""
		for _, c := range selected.Changes {
			if !validPath(c.Path) || len(c.Content) > 64<<10 || files[c.Path] != nil {
				return fmt.Errorf("invalid or duplicate change path")
			}
			old, err := e.api.File(ctx, inst, repo, c.Path, base)
			if err != nil {
				return err
			}
			if hash(old) != c.Before {
				return fmt.Errorf("preimage mismatch: %s", c.Path)
			}
			files[c.Path] = []byte(c.Content)
			diff += "--- a/" + c.Path + "\n+++ b/" + c.Path + "\n"
			a := strings.Split(strings.TrimSuffix(string(old), "\n"), "\n")
			b := strings.Split(strings.TrimSuffix(c.Content, "\n"), "\n")
			diff += fmt.Sprintf("@@ -1,%d +1,%d @@\n", len(a), len(b))
			for _, l := range a {
				diff += "-" + l + "\n"
			}
			for _, l := range b {
				diff += "+" + l + "\n"
			}
		}
		if len(diff) > 24000 {
			return fmt.Errorf("diff too large for issue review")
		}
		for _, check := range selected.Checks {
			content, ok := files[check.Path]
			if !ok {
				return fmt.Errorf("check must target a changed file")
			}
			var doc map[string]any
			if json.Unmarshal(content, &doc) != nil {
				return fmt.Errorf("JSON check requires object")
			}
			got, ok := doc[check.Key]
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(check.Equals)
			if !ok || string(a) != string(b) {
				return fmt.Errorf("declared JSON check failed: %s/%s", check.Path, check.Key)
			}
		}
		encoded, _ := json.Marshal(files)
		p.History = append(p.History, p.Choice+":"+p.PatchHash)
		if ev.Comment.ID > 0 {
			p.LastComment = ev.Comment.ID
		}
		p.Pending = false
		p.Choice = selected.ID
		p.PatchHash = hash(encoded)
		p.Files = files
		p.Diff = diff
		p.PR = ""
		if err = e.save(); err != nil {
			return err
		}
		msg := fmt.Sprintf("### Selected `%s` · plan `%s`\n\n```diff\n%s```\n\n%d repository-defined JSON checks passed. Behavioral sandbox checks: not connected.\n\nPatch `%s`\n\n- [ ] Open draft pull request", p.Choice, p.ID, diff, len(selected.Checks), p.PatchHash)
		if api, ok := e.api.(nativeAPI); ok {
			comment, err := api.ControlComment(ctx, inst, repo, n, msg)
			if err != nil {
				return err
			}
			p.PublishComment, p.PublishBody = comment, msg
			return e.save()
		}
		return e.api.Comment(ctx, inst, repo, n, msg)
	}
	if len(parts) != 4 || p.Choice == "" || parts[3] != p.PatchHash[:12] {
		return fmt.Errorf("publish requires the current selected patch hash")
	}
	if p.PR != "" {
		return e.ensureCheck(ctx, inst, repo, p)
	}
	encoded, _ := json.Marshal(p.Files)
	if hash(encoded) != p.PatchHash {
		return fmt.Errorf("patch bytes changed")
	}
	appBranch := fmt.Sprintf("trajectory/issue-%d-%s-%s", n, p.ID, p.PatchHash[:12])
	if appBranch == branch {
		return fmt.Errorf("cannot publish to default branch")
	}
	p.Pending = true
	if err = e.save(); err != nil {
		return err
	}
	guard := func(ctx context.Context) error {
		t, b, err := e.api.Issue(ctx, inst, repo, n)
		if err != nil {
			return err
		}
		if issueHash(t, b) != p.IssueDigest {
			return fmt.Errorf("issue changed during publication")
		}
		return nil
	}
	if err = guard(ctx); err != nil {
		return err
	}
	ctx = WithPublicationGuard(ctx, guard)
	pr, err := e.api.Publish(ctx, inst, repo, p.BaseBranch, p.BaseSHA, appBranch, "Trajectory: "+title, fmt.Sprintf("Resolves #%d\n\nOwner: @%s\nDecision: %s\nPlan: %s\nBase: %s\nPatch SHA-256: %s\n\nRepository-defined preimage and JSON checks passed. Behavioral sandbox checks are not connected.\n", n, ev.Sender.Login, p.Choice, p.ID, p.BaseSHA, p.PatchHash), p.Files)
	if err != nil {
		return err
	}
	p.PR = pr
	p.Pending = false
	if ev.Comment.ID > 0 {
		p.LastComment = ev.Comment.ID
	}
	if err = e.save(); err != nil {
		return err
	}
	if err := e.ensureCheck(ctx, inst, repo, p); err != nil {
		return err
	}
	return e.api.Comment(ctx, inst, repo, n, "Draft PR: "+pr+"\nPatch: `"+p.PatchHash+"`")
}
