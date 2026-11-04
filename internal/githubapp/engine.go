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

	"github.com/instagrim-dev/proofcell-vultr/internal/repair"
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
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Changes     []Change `json:"changes"`
	Checks      []Check  `json:"checks"`
}
type Config struct {
	Delivery   string   `json:"delivery,omitempty"`
	Generation string   `json:"generation,omitempty"`
	Version    int      `json:"version"`
	Question   string   `json:"question"`
	Choices    []Choice `json:"choices"`
}
type Plan struct {
	SandboxReruns                                                     int
	StatusCommentID                                                   int64
	PreviousChoice                                                    string
	OutcomeAnnounced                                                  bool
	IntentCheckID                                                     int64
	PreviousPR, IntentConfirmedBy, IntentTitle                        string
	Generated                                                         *repair.Result
	DecisionComment, PublishComment, CheckID                          int64
	DecisionBody, PublishBody, PublishPatch, CheckHead                string
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
	CheckSuite struct {
		ID      int64  `json:"id"`
		HeadSHA string `json:"head_sha"`
	} `json:"check_suite"`
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
		Labels      []struct {
			Name string `json:"name"`
		} `json:"labels"`
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
	ReportIssue   int
}
type disk struct {
	Plans map[string]*Plan
	Jobs  map[string]*Job
}
type Repairer interface {
	Repair(context.Context, string, []byte) (repair.Result, error)
}
type Engine struct {
	Repairer Repairer
	mu       sync.Mutex
	dir      string
	api      API
	data     disk
	wake     chan struct{}
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
		openedTrajectory := false
		if ev.Kind == "issues" && ev.Action == "opened" {
			for _, label := range ev.Issue.Labels {
				if label.Name == "trajectory" {
					openedTrajectory = true
					break
				}
			}
		}
		isIssue := ev.Kind == "issues" && (openedTrajectory || (ev.Action == "labeled" && ev.Label.Name == "trajectory") || ev.Action == "edited" || ev.Action == "closed")
		isComment := ev.Kind == "issue_comment" && ((ev.Action == "created" && ev.Comment.User.Type != "Bot" && strings.HasPrefix(strings.TrimSpace(ev.Comment.Body), "/trajectory ")) || ev.Action == "edited") && (len(ev.Issue.PullRequest) == 0 || string(ev.Issue.PullRequest) == "null")
		isCheck := (ev.Kind == "check_run" && (ev.Action == "requested_action" || ev.Action == "rerequested")) || (ev.Kind == "check_suite" && ev.Action == "rerequested")
		if !isIssue && !isComment && !isCheck {
			w.WriteHeader(202)
			return
		}
		id := r.Header.Get("X-GitHub-Delivery")
		if id == "" || len(id) > 200 || ev.Installation.ID <= 0 || (!isCheck && ev.Issue.Number <= 0) || (ev.Kind == "check_run" && isCheck && ev.CheckRun.ID <= 0) || (ev.Kind == "check_suite" && isCheck && (ev.CheckSuite.ID <= 0 || ev.CheckSuite.HeadSHA == "")) || !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(ev.Repository.FullName) {
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
		if j.Status == "done" || (j.Attempts >= 3 && j.Status != "reporting") {
			continue
		}
		if j.Status != "reporting" {
			j.Attempts++
			call, cancel := context.WithTimeout(ctx, 240*time.Second)
			err = e.process(call, j.Event)
			cancel()
			if err != nil {
				j.Status, j.Error = "failed", err.Error()
				if j.Attempts == 3 {
					j.ReportIssue = j.Event.Issue.Number
					if j.Event.Kind == "check_run" {
						j.ReportIssue = e.checkIssue(j.Event)
					}
					if j.ReportIssue > 0 {
						j.Status = "reporting"
					}
				}
			} else {
				j.Status, j.Error = "done", ""
			}
		}
		if j.Status == "reporting" {
			call, cancel := context.WithTimeout(ctx, 30*time.Second)
			p := e.data.Plans[fmt.Sprintf("%d/%s/%d", j.Event.Installation.ID, j.Event.Repository.FullName, j.ReportIssue)]
			if p != nil && p.StatusCommentID != 0 {
				err = e.deliveryStatus(call, j.Event.Installation.ID, j.Event.Repository.FullName, j.ReportIssue, p, "Needs attention", "Could not complete: "+j.Error+". Retry after correcting the inputs.")
			} else {
				err = e.api.Comment(call, j.Event.Installation.ID, j.Event.Repository.FullName, j.ReportIssue, "Trajectory could not complete this action: "+j.Error+". Correct the inputs or restore service, then try again.")
			}
			cancel()
			if err == nil {
				j.Status = "failed"
			}
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
	nativeApproval := false
	if ev.Kind == "check_suite" && ev.Action == "rerequested" {
		prefix := fmt.Sprintf("%d/%s/", inst, repo)
		for key, p := range e.data.Plans {
			if strings.HasPrefix(key, prefix) && p.CheckHead == ev.CheckSuite.HeadSHA && p.CheckID > 0 {
				next := ev
				next.Kind = "check_run"
				next.CheckRun.ID = p.CheckID
				next.CheckRun.HeadSHA = p.CheckHead
				return e.processCheck(ctx, next)
			}
		}
		return nil
	}
	if ev.Kind == "check_run" {
		return e.processCheck(ctx, ev)
	}
	if ev.Kind == "issues" && (ev.Action == "edited" || ev.Action == "closed") {
		p := e.data.Plans[fmt.Sprintf("%d/%s/%d", inst, repo, n)]
		if p == nil {
			return nil
		}
		title, body, err := e.api.Issue(ctx, inst, repo, n)
		if err != nil {
			return err
		}
		if ev.Action == "closed" || issueHash(title, body) != p.IssueDigest {
			return e.invalidateIntent(ctx, inst, repo, n, p, "Issue intent changed; select a new decision")
		}
		return nil
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
		if ev.Comment.ID != p.DecisionComment && (p.PublishComment == 0 || ev.Comment.ID != p.PublishComment) {
			return nil
		}
		allowed, err := e.api.CanWrite(ctx, inst, repo, ev.Sender.Login)
		if err != nil {
			return err
		}
		if !allowed {
			return fmt.Errorf("action requires current repository write permission")
		}
		reader, hasReader := e.api.(interface {
			CurrentComment(context.Context, int64, string, int64) (string, error)
		})
		if hasReader && (ev.Comment.ID == p.DecisionComment || ev.Comment.ID == p.PublishComment) {
			current, err := reader.CurrentComment(ctx, inst, repo, ev.Comment.ID)
			if err != nil {
				return err
			}
			if current != ev.Comment.Body {
				// A queued clear is still a real decision transition, even when
				// the owner has already selected again. Do not process the stale
				// choice, but revoke any confirmation based on the old selection.
				if ev.Comment.ID == p.DecisionComment && ev.Comment.Body == p.DecisionBody && p.IntentConfirmedBy != "" {
					if err := e.invalidateIntent(ctx, inst, repo, n, p, "Decision selection changed"); err != nil {
						return err
					}
					if err := e.ensureDeliveryChecks(ctx, inst, repo, n, p); err != nil {
						return err
					}
					return e.announceOutcome(ctx, inst, repo, n, p)
				}
				return nil
			}
		}
		switch ev.Comment.ID {
		case p.DecisionComment:
			if p.DecisionComment == 0 || (p.PR != "" && p.Config.Delivery != "on-decision") {
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
				if err := e.invalidateIntent(ctx, inst, repo, n, p, "Decision selection changed"); err != nil {
					return err
				}
				p.PublishComment, p.PublishBody, p.PublishPatch = 0, "", ""
				if err := e.save(); err != nil {
					return err
				}
				if p.Config.Delivery == "on-decision" && p.StatusCommentID != 0 {
					if err := e.deliveryStatus(ctx, inst, repo, n, p, "No outcome selected", "The earlier intent confirmation is invalidated. Select one outcome to continue."); err != nil {
						return err
					}
				}
				if strings.Count(strings.ToLower(ev.Comment.Body), "[x]") > 1 {
					return e.api.Comment(ctx, inst, repo, n, "Select one option: clear the other checkbox first.")
				}
				return nil
			}
			ev.Comment.Body = "/trajectory choose " + p.ID + " " + choice
		case p.PublishComment:
			if p.PublishComment == 0 || p.PublishPatch == "" || p.PublishPatch != p.PatchHash || ev.Comment.Body != strings.Replace(p.PublishBody, "- [ ] Open draft pull request", "- [x] Open draft pull request", 1) {
				return nil
			}
			if hasReader {
				current, err := reader.CurrentComment(ctx, inst, repo, p.DecisionComment)
				if err != nil {
					return err
				}
				match := false
				for _, c := range p.Config.Choices {
					if c.ID == p.Choice {
						line := "- [ ] " + c.Title + " <!-- " + c.ID + " -->"
						match = current == strings.Replace(p.DecisionBody, line, strings.Replace(line, "[ ]", "[x]", 1), 1)
					}
				}
				if !match {
					return fmt.Errorf("decision changed; review the latest choice before publishing")
				}
			}
			nativeApproval = hasReader
			ev.Comment.Body = "/trajectory publish " + p.ID + " " + p.PublishPatch[:12]
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
		if cfg.Delivery != "" && cfg.Delivery != "on-decision" {
			return fmt.Errorf("unsupported delivery mode")
		}
		if cfg.Generation != "" && cfg.Generation != "vultr-go-access-v1" {
			return fmt.Errorf("unsupported generation mode")
		}
		if cfg.Version != 1 || cfg.Question == "" || len(cfg.Choices) < 2 || len(cfg.Choices) > 8 {
			return fmt.Errorf("expected version 1, question, and 2–8 choices")
		}
		ids := map[string]bool{}
		for _, c := range cfg.Choices {
			if !regexp.MustCompile(`^[a-z0-9-]{1,40}$`).MatchString(c.ID) || ids[c.ID] || c.Title == "" || strings.ContainsAny(c.Title, "\r\n<>") || len(c.Description) > 500 || strings.ContainsAny(c.Description, "\r\n<>") || len(c.Changes) == 0 || len(c.Changes) > 10 || len(c.Checks) == 0 {
				return fmt.Errorf("invalid choice")
			}
			ids[c.ID] = true
		}
		id := hash([]byte(issueHash(title, body) + base + hash(b)))[:12]
		if p != nil && p.ID == id && p.DecisionComment != 0 {
			if p.Config.Delivery == "on-decision" && p.IntentConfirmedBy != "" {
				if err := e.currentSelection(ctx, inst, repo, p); err != nil {
					if invalidErr := e.invalidateIntent(ctx, inst, repo, n, p, "Decision control changed"); invalidErr != nil {
						return invalidErr
					}
					return err
				}
			}
			if ev.Comment.ID > 0 {
				p.LastComment = ev.Comment.ID
			}
			return e.save()
		}
		if p == nil || p.ID != id {
			if p != nil {
				if err := e.invalidateIntent(ctx, inst, repo, n, p, "Superseded by a new intent revision"); err != nil {
					return err
				}
			}
			p = &Plan{IntentTitle: title, ID: id, IssueDigest: issueHash(title, body), BaseBranch: branch, BaseSHA: base, Config: cfg}
		}
		if ev.Comment.ID > 0 {
			p.LastComment = ev.Comment.ID
		}
		e.data.Plans[key] = p
		if err = e.save(); err != nil {
			return err
		}
		msg := decisionComment(id, cfg)
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
		if p.Config.Delivery == "on-decision" {
			if p.Pending && p.Choice == selected.ID && p.PR == "" && p.PatchHash != "" {
				next := ev
				next.Kind, next.Action = "", ""
				next.Comment.Body = "/trajectory publish " + p.ID + " " + p.PatchHash[:12]
				return e.process(ctx, next)
			}
			if p.Choice == selected.ID && p.PR != "" {
				if err := e.ensureCheck(ctx, inst, repo, p); err != nil {
					return err
				}
				return e.announceOutcome(ctx, inst, repo, n, p)
			}
			if err := e.invalidateIntent(ctx, inst, repo, n, p, "Superseded by a new decision"); err != nil {
				return err
			}
		}
		if p.Config.Delivery == "on-decision" {
			if err := e.deliveryStatus(ctx, inst, repo, n, p, "Preparing", "Preparing the selected outcome and its verification."); err != nil {
				return err
			}
		}
		files := map[string][]byte{}
		var generated *repair.Result
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
			content := []byte(c.Content)
			if p.Config.Generation == "vultr-go-access-v1" && c.Path == "access.go" {
				if e.Repairer == nil {
					return fmt.Errorf("Vultr generation worker not configured")
				}
				result, err := e.Repairer.Repair(ctx, selected.ID, old)
				if err != nil {
					return err
				}
				if hash(result.Source) != result.SourceSHA256 || !validBoundaryReceipt(selected.ID, result.Evidence) || result.RequestID == "" {
					return fmt.Errorf("invalid generated candidate receipt")
				}
				generated = &result
				content = result.Source
			}
			files[c.Path] = content
			diff += "--- a/" + c.Path + "\n+++ b/" + c.Path + "\n"
			a := strings.Split(strings.TrimSuffix(string(old), "\n"), "\n")
			b := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
			diff += fmt.Sprintf("@@ -1,%d +1,%d @@\n", len(a), len(b))
			for _, l := range a {
				diff += "-" + l + "\n"
			}
			for _, l := range b {
				diff += "+" + l + "\n"
			}
		}
		if p.Config.Generation != "" && generated == nil {
			return fmt.Errorf("generation requires an access.go change")
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
		if p.Config.Generation == "vultr-go-access-v1" && selected.ID == "refund" {
			if err := verifyExactProration(files["subscription.json"]); err != nil {
				return err
			}
		}
		encoded, _ := json.Marshal(files)
		p.History = append(p.History, p.Choice+":"+p.PatchHash)
		if ev.Comment.ID > 0 {
			p.LastComment = ev.Comment.ID
		}
		p.Pending = false
		p.PublishComment, p.PublishBody, p.PublishPatch = 0, "", ""
		p.PreviousChoice = p.Choice
		p.Choice = selected.ID
		p.PatchHash = hash(encoded)
		p.Files = files
		p.Generated = generated
		p.Diff = diff
		p.OutcomeAnnounced = false
		p.PreviousPR = p.PR
		p.PR = ""
		p.CheckID, p.CheckHead = 0, ""
		p.IntentCheckID, p.IntentConfirmedBy = 0, ""
		if err = e.save(); err != nil {
			return err
		}
		if p.Config.Delivery == "on-decision" {
			next := ev
			next.Kind, next.Action = "", ""
			next.Comment.Body = "/trajectory publish " + p.ID + " " + p.PatchHash[:12]
			return e.process(ctx, next)
		}
		verification := "Behavioral sandbox checks: not connected."
		if p.Generated != nil {
			verification = fmt.Sprintf("Sandbox boundary tests passed. Vultr model `%s`, response `%s`, candidate SHA-256 `%s`, tests SHA-256 `%s`. Host canary preserved; container removed.", p.Generated.Model, p.Generated.RequestID, p.Generated.SourceSHA256, p.Generated.Evidence.TestsSHA256)
		}
		if p.Generated != nil {
			verification += "\n\n<details><summary>Sandbox test output</summary>\n\n```text\n" + strings.ReplaceAll(p.Generated.Evidence.Output, "```", "'&#96;'&#96;'&#96;") + "\n```\n</details>"
		}
		msg := fmt.Sprintf("### Selected `%s` · plan `%s`\n\n```diff\n%s```\n\n%d repository-defined JSON checks passed. %s\n\nPatch `%s`\n\n- [ ] Open draft pull request", p.Choice, p.ID, diff, len(selected.Checks), verification, p.PatchHash)
		if api, ok := e.api.(nativeAPI); ok {
			comment, err := api.ControlComment(ctx, inst, repo, n, msg)
			if err != nil {
				return err
			}
			p.PublishComment, p.PublishBody, p.PublishPatch = comment, msg, p.PatchHash
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
		if p.Config.Delivery == "on-decision" {
			if err := e.currentSelection(ctx, inst, repo, p); err != nil {
				return err
			}
		}
		if nativeApproval {
			reader := e.api.(interface {
				CurrentComment(context.Context, int64, string, int64) (string, error)
			})
			current, err := reader.CurrentComment(ctx, inst, repo, p.DecisionComment)
			if err != nil {
				return err
			}
			match := false
			for _, c := range p.Config.Choices {
				if c.ID == p.Choice {
					line := "- [ ] " + c.Title + " <!-- " + c.ID + " -->"
					match = current == strings.Replace(p.DecisionBody, line, strings.Replace(line, "[ ]", "[x]", 1), 1)
				}
			}
			if !match {
				return fmt.Errorf("decision changed during publication")
			}
			approval, err := reader.CurrentComment(ctx, inst, repo, p.PublishComment)
			if err != nil {
				return err
			}
			if p.PublishPatch != p.PatchHash || approval != strings.Replace(p.PublishBody, "- [ ] Open draft pull request", "- [x] Open draft pull request", 1) {
				return fmt.Errorf("approval changed during publication")
			}
		}
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
	prBody := fmt.Sprintf("Resolves #%d\n\nOwner: @%s\nDecision: %s\nPlan: %s\nBase: %s\nPatch SHA-256: %s\n\n%s\n", n, ev.Sender.Login, p.Choice, p.ID, p.BaseSHA, p.PatchHash, checkSummary(p))
	if p.Config.Delivery == "on-decision" {
		prBody = fmt.Sprintf("Resolves #%d\n\n**Selected outcome:** %s\n\nReview the patch in Files changed. The Checks tab contains independent verification, full evidence, and the intent confirmation. Confirm that the evidence resolves the issue before using GitHub's normal merge workflow.", n, selectedTitle(p))
	}
	prTitle := "Trajectory: " + title
	if p.Config.Delivery == "on-decision" {
		prTitle = "Trajectory: " + selectedTitle(p) + " — " + title
	}
	pr, err := e.api.Publish(ctx, inst, repo, p.BaseBranch, p.BaseSHA, appBranch, prTitle, prBody, p.Files)
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
	if p.Config.Delivery == "on-decision" {
		return e.announceOutcome(ctx, inst, repo, n, p)
	}
	return e.api.Comment(ctx, inst, repo, n, "Draft PR: "+pr+"\nPatch: `"+p.PatchHash+"`")
}
