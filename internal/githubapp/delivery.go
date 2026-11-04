package githubapp

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/instagrim-dev/proofcell-vultr/internal/repair"
)

type CheckAction struct {
	Label       string `json:"label"`
	Description string `json:"description"`
	Identifier  string `json:"identifier"`
}
type CheckReport struct {
	ID                               int64  `json:"-"`
	Head                             string `json:"head_sha"`
	Key                              string `json:"external_id"`
	Name                             string `json:"name"`
	Conclusion                       string `json:"conclusion"`
	Title, Summary, Text, DetailsURL string
	Actions                          []CheckAction     `json:"actions"`
	Annotations                      []CheckAnnotation `json:"-"`
}
type CheckAnnotation struct {
	Path            string `json:"path"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	AnnotationLevel string `json:"annotation_level"`
	Title           string `json:"title"`
	Message         string `json:"message"`
}

func proofAnnotations(p *Plan) []CheckAnnotation {
	if p.Generated == nil {
		return nil
	}
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "access.go", p.Files["access.go"], 0)
	if err != nil {
		return nil
	}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "HasAccess" {
			line := fs.Position(fn.Pos()).Line
			return []CheckAnnotation{{Path: "access.go", StartLine: line, EndLine: line, AnnotationLevel: "notice", Title: "Access behavior checked", Message: "Independent sandbox tests passed for the four tested expiry-boundary inputs. See the Verification check for details."}}
		}
	}
	return nil
}

type deliveryAPI interface {
	PutDeliveryCheck(context.Context, int64, string, CheckReport) (int64, error)
}

func selectedTitle(p *Plan) string {
	for _, c := range p.Config.Choices {
		if c.ID == p.Choice {
			return c.Title
		}
	}
	return p.Choice
}
func deliverySummary(repo string, n int, p *Plan) string {
	return fmt.Sprintf("**Intent:** [#%d — %s](https://github.com/%s/issues/%d)\n\n**Decision:** %s\n\n**Outcome:** [Draft pull request](%s)\n\nQuestion: %s", n, p.IntentTitle, repo, n, selectedTitle(p), p.PR, p.Config.Question)
}
func validBoundaryReceipt(choice string, ev repair.Evidence) bool {
	if repair.BoundaryTestsSHA256(choice) == "" || ev.TestsSHA256 != repair.BoundaryTestsSHA256(choice) {
		return false
	}
	out := ev.Output
	for _, name := range []string{"before_expiry", "different_expiry", "at_expiry", "after_expiry"} {
		if !strings.Contains(out, "--- PASS: TestBoundary/"+name+" ") {
			return false
		}
	}
	return ev.CanaryPreserved && ev.CleanupConfirmed
}
func proofSummary(p *Plan) string {
	if p.Generated == nil || !validBoundaryReceipt(p.Choice, p.Generated.Evidence) {
		return ""
	}
	before := "denied"
	if p.Choice == "keep" {
		before = "allowed"
	}
	proration := ""
	if p.Choice == "refund" && verifyExactProration(p.Files["subscription.json"]) == nil {
		proration = "\n\n**Refund example:** An independent exact-proration calculation passed for the recorded values. This does not execute a refund; non-integral amounts and rounding are not tested."
	}
	return fmt.Sprintf("\n\n### What was checked\n\n| Expected in the access function | Observed in independent sandbox tests |\n| --- | --- |\n| Access is %s just before expiry. | Passed for both tested inputs (2/2). |\n| Access is denied at expiry and just after expiry. | Passed for both tested inputs (2/2). |%s\n\nFor PR head `%s`, configured JSON assertions and the PR tree comparison also passed. These tests exercise a bounded function, not a live customer account or billing system.", before, proration, p.CheckHead)
}
func proofText(p *Plan) string {
	text := fmt.Sprintf("### Binding\n\n| Evidence | Recorded value |\n|---|---|\n| Issue digest | `%s` |\n| Plan | `%s` |\n| Base commit | `%s` |\n| Patched commit | `%s` |\n| Patch SHA-256 | `%s` |\n\n### Verification scope\n\nRepository-defined file preimages and JSON assertions, plus exact PR tree comparison.\n", p.IssueDigest, p.ID, p.BaseSHA, p.CheckHead, p.PatchHash)
	if p.Generated != nil {
		g := p.Generated
		text += fmt.Sprintf("\nIndependent sandbox checks the bounded access function at expiry boundaries.\n\n| Provenance | Value |\n|---|---|\n| Model | `%s` |\n| Inference response | `%s` |\n| Candidate SHA-256 | `%s` |\n| Test SHA-256 | `%s` |\n| Host canary preserved | `%t` |\n| Sandbox removed | `%t` |\n\n### Actual test output\n\n```text\n%s\n```\n", g.Model, g.RequestID, g.SourceSHA256, g.Evidence.TestsSHA256, g.Evidence.CanaryPreserved, g.Evidence.CleanupConfirmed, strings.ReplaceAll(g.Evidence.Output, "```", "'''"))
	}
	rerun := ""
	if p.SandboxReruns > 0 {
		rerun = "\n\nSandbox re-run: the current candidate was independently tested again without generating a new patch."
	}
	return "### Outcome and observed checks\n\n**Selected outcome:** " + selectedTitle(p) + "\n\nThe declared JSON checks and exact PR tree comparison passed." + func() string {
		if p.Generated != nil {
			return " Independent sandbox boundary tests passed; the host canary remained intact and the container was removed."
		}
		return ""
	}() + rerun + "\n\n" + text + "\nValidate linkage rechecks the current issue, selected outcome, base and PR tree against this receipt; it does not rerun the sandbox."
}
func intentReport(repo string, n int, p *Plan, conclusion, reason string) CheckReport {
	reviewer := "Awaiting review"
	if p.IntentConfirmedBy != "" {
		reviewer = "@" + p.IntentConfirmedBy
	}
	actions := []CheckAction{}
	if conclusion == "action_required" {
		actions = append(actions, CheckAction{"Confirm intent", "Connect this proof to the issue intent", "confirm_intent"})
	}
	return CheckReport{ID: p.IntentCheckID, Head: p.CheckHead, Key: p.ID + ":" + p.PatchHash + ":intent", Name: "Trajectory / Intent", Conclusion: conclusion, Title: reason, Summary: deliverySummary(repo, n, p), Text: "Review the selected outcome and [full verification evidence](" + p.PR + "/checks?check_run_id=" + strconv.FormatInt(p.CheckID, 10) + ").\n\nIntent reviewer: " + reviewer + "\n\nConfirm intent means the reviewer has assessed whether this proof resolves the linked issue. Merge uses GitHub's normal PR review and branch rules.", DetailsURL: fmt.Sprintf("https://github.com/%s/issues/%d", repo, n), Actions: actions}
}

func (e *Engine) invalidateIntent(ctx context.Context, inst int64, repo string, n int, p *Plan, reason string) error {
	if p.Config.Delivery != "on-decision" || p.IntentCheckID == 0 {
		return nil
	}
	api, ok := e.api.(deliveryAPI)
	if !ok {
		return fmt.Errorf("delivery checks unavailable")
	}
	p.IntentConfirmedBy = ""
	_, err := api.PutDeliveryCheck(ctx, inst, repo, intentReport(repo, n, p, "cancelled", reason))
	if err != nil {
		return err
	}
	return e.save()
}
func (e *Engine) currentSelection(ctx context.Context, inst int64, repo string, p *Plan) error {
	reader, ok := e.api.(interface {
		CurrentComment(context.Context, int64, string, int64) (string, error)
	})
	if !ok {
		return nil
	}
	current, err := reader.CurrentComment(ctx, inst, repo, p.DecisionComment)
	if err != nil {
		return err
	}
	for _, c := range p.Config.Choices {
		if c.ID == p.Choice {
			line := "- [ ] " + c.Title + " <!-- " + c.ID + " -->"
			if current == strings.Replace(p.DecisionBody, line, strings.Replace(line, "[ ]", "[x]", 1), 1) {
				return nil
			}
		}
	}
	return fmt.Errorf("selected outcome changed")
}
func (e *Engine) ensureDeliveryChecks(ctx context.Context, inst int64, repo string, n int, p *Plan) error {
	api, ok := e.api.(deliveryAPI)
	if !ok {
		return fmt.Errorf("delivery checks unavailable")
	}
	native := e.api.(nativeAPI)
	head, err := native.PRHead(ctx, inst, repo, p.PR)
	if err != nil {
		return err
	}
	if err = native.VerifyPRPatch(ctx, inst, repo, p.BaseSHA, head, p.Files); err != nil {
		return err
	}
	if p.CheckHead != "" && p.CheckHead != head {
		return fmt.Errorf("PR head changed")
	}
	p.CheckHead = head
	conclusion := "neutral"
	scope := "Declared JSON and patch checks passed"
	if p.Generated != nil {
		if hash(p.Generated.Source) != p.Generated.SourceSHA256 || hash(p.Files["access.go"]) != p.Generated.SourceSHA256 || !validBoundaryReceipt(p.Choice, p.Generated.Evidence) {
			if p.CheckID != 0 {
				_, updateErr := api.PutDeliveryCheck(ctx, inst, repo, CheckReport{ID: p.CheckID, Head: head, Key: p.ID + ":" + p.PatchHash + ":verification", Name: "Trajectory / Verification", Conclusion: "failure", Title: "Sandbox evidence could not be verified", Summary: deliverySummary(repo, n, p) + "\n\nThe sandbox receipt does not match the recognized test suite and recorded passing cases.", DetailsURL: p.PR + "/files"})
				if updateErr != nil {
					return updateErr
				}
			}
			if err := e.invalidateIntent(ctx, inst, repo, n, p, "Verification evidence could not be verified"); err != nil {
				return err
			}
			return fmt.Errorf("invalid sandbox receipt")
		}
		if p.Config.Generation == "vultr-go-access-v1" && p.Choice == "refund" {
			if err := verifyExactProration(p.Files["subscription.json"]); err != nil {
				return err
			}
		}
		conclusion = "success"
		scope = "Independent boundary checks and patch linkage passed"
	}
	id, err := api.PutDeliveryCheck(ctx, inst, repo, CheckReport{ID: p.CheckID, Head: head, Key: p.ID + ":" + p.PatchHash + ":verification", Name: "Trajectory / Verification", Conclusion: conclusion, Title: scope, Summary: deliverySummary(repo, n, p) + proofSummary(p), Text: proofText(p), Annotations: proofAnnotations(p), DetailsURL: p.PR + "/files", Actions: []CheckAction{{"Validate linkage", "Recheck intent, decision and PR tree", "revalidate"}}})
	if err != nil {
		return err
	}
	p.CheckID = id
	if err = e.save(); err != nil {
		return err
	}
	state, title := "action_required", "Review the proof against the issue intent"
	if p.IntentConfirmedBy != "" {
		state, title = "success", "Intent confirmed by @"+p.IntentConfirmedBy
	}
	id, err = api.PutDeliveryCheck(ctx, inst, repo, intentReport(repo, n, p, state, title))
	if err != nil {
		return err
	}
	p.IntentCheckID = id
	return e.save()
}
func (e *Engine) processDeliveryCheck(ctx context.Context, ev Event, n int, p *Plan) error {
	if ev.CheckRun.HeadSHA != p.CheckHead {
		return nil
	}
	inst, repo := ev.Installation.ID, ev.Repository.FullName
	allowed, err := e.api.CanWrite(ctx, inst, repo, ev.Sender.Login)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("current repository write permission required")
	}
	if ev.Action != "rerequested" && ev.RequestedAction.Identifier != "confirm_intent" && ev.RequestedAction.Identifier != "revalidate" {
		return nil
	}
	validate := func() error {
		title, body, err := e.api.Issue(ctx, inst, repo, n)
		if err != nil {
			return err
		}
		if issueHash(title, body) != p.IssueDigest {
			return fmt.Errorf("issue intent changed")
		}
		_, base, err := e.api.Repository(ctx, inst, repo)
		if err != nil {
			return err
		}
		if base != p.BaseSHA {
			return fmt.Errorf("base changed")
		}
		if err = e.currentSelection(ctx, inst, repo, p); err != nil {
			return err
		}
		native := e.api.(nativeAPI)
		head, err := native.PRHead(ctx, inst, repo, p.PR)
		if err != nil {
			return err
		}
		if head != p.CheckHead {
			return fmt.Errorf("PR head changed")
		}
		encoded, _ := json.Marshal(p.Files)
		if hash(encoded) != p.PatchHash {
			return fmt.Errorf("patch bytes changed")
		}
		return native.VerifyPRPatch(ctx, inst, repo, p.BaseSHA, head, p.Files)
	}
	if err = validate(); err != nil {
		_ = e.invalidateIntent(ctx, inst, repo, n, p, "Inputs changed: "+err.Error())
		return err
	}
	if ev.Action == "rerequested" && ev.CheckRun.ID == p.CheckID && p.Generated != nil {
		worker, ok := e.Repairer.(interface {
			Verify(context.Context, string, []byte) (repair.Evidence, error)
		})
		var evidence repair.Evidence
		var err error
		if ok {
			evidence, err = worker.Verify(ctx, p.Choice, p.Generated.Source)
		} else {
			err = fmt.Errorf("sandbox unavailable")
		}
		if err != nil || !validBoundaryReceipt(p.Choice, evidence) {
			_ = e.invalidateIntent(ctx, inst, repo, n, p, "Sandbox re-run failed")
			api := e.api.(deliveryAPI)
			_, updateErr := api.PutDeliveryCheck(ctx, inst, repo, CheckReport{ID: p.CheckID, Head: p.CheckHead, Key: p.ID + ":" + p.PatchHash + ":verification", Name: "Trajectory / Verification", Conclusion: "failure", Title: "Sandbox re-run could not be verified", Summary: "The existing candidate could not be verified by a fresh independent sandbox run.", DetailsURL: p.PR + "/files"})
			if updateErr != nil {
				return updateErr
			}
			return nil
		}
		p.Generated.Evidence = evidence
		p.SandboxReruns++
		if err := e.save(); err != nil {
			return err
		}
	}
	if ev.RequestedAction.Identifier == "confirm_intent" {
		if ev.CheckRun.ID != p.IntentCheckID {
			return fmt.Errorf("use the Intent check to confirm")
		}
		p.IntentConfirmedBy = ev.Sender.Login
	}
	if err := e.ensureDeliveryChecks(ctx, inst, repo, n, p); err != nil {
		return err
	}
	if (ev.RequestedAction.Identifier == "confirm_intent" || ev.Action == "rerequested") && p.StatusCommentID != 0 {
		return e.announceOutcome(ctx, inst, repo, n, p)
	}
	return nil
}

// PutDeliveryCheck reconciles a receipt on a specific commit before writing it.
func (c *Client) PutDeliveryCheck(ctx context.Context, inst int64, repo string, r CheckReport) (int64, error) {
	token, err := c.installationToken(ctx, inst, repo)
	if err != nil {
		return 0, err
	}
	if r.Head == "" || r.Key == "" || r.Name == "" || len(r.Text) > 65535 || len(r.Summary) > 65535 {
		return 0, fmt.Errorf("invalid delivery check")
	}
	for _, a := range r.Actions {
		if len(a.Label) > 20 || len(a.Description) > 40 || len(a.Identifier) > 20 {
			return 0, fmt.Errorf("invalid check action")
		}
	}
	if r.ID == 0 {
		var list struct {
			Checks []struct {
				ID         int64
				ExternalID string `json:"external_id"`
				Head       string `json:"head_sha"`
			} `json:"check_runs"`
		}
		route := repositoryRoute(repo) + "/commits/" + url.PathEscape(r.Head) + "/check-runs?check_name=" + url.QueryEscape(r.Name) + "&filter=all&per_page=100"
		if err = c.request(ctx, token, http.MethodGet, route, nil, &list); err != nil {
			return 0, err
		}
		for _, existing := range list.Checks {
			if existing.ExternalID == r.Key && existing.Head == r.Head {
				r.ID = existing.ID
				break
			}
		}
		if r.ID == 0 && len(list.Checks) >= 100 {
			return 0, fmt.Errorf("cannot reconcile check in truncated result")
		}
	}
	method, route := http.MethodPost, repositoryRoute(repo)+"/check-runs"
	if r.ID > 0 {
		method = http.MethodPatch
		route += "/" + strconv.FormatInt(r.ID, 10)
	}
	output := map[string]any{"title": r.Title, "summary": r.Summary, "text": r.Text}
	if r.ID == 0 && len(r.Annotations) > 0 {
		output["annotations"] = r.Annotations
	}
	body := map[string]any{"name": r.Name, "head_sha": r.Head, "external_id": r.Key, "status": "completed", "conclusion": r.Conclusion, "details_url": r.DetailsURL, "output": output, "actions": r.Actions}
	if method == http.MethodPatch {
		delete(body, "head_sha")
	}
	var out struct{ ID int64 }
	if err = c.request(ctx, token, method, route, body, &out); err != nil {
		return 0, err
	}
	if out.ID <= 0 {
		return 0, fmt.Errorf("invalid delivery check ID")
	}
	return out.ID, nil
}

func (e *Engine) announceOutcome(ctx context.Context, inst int64, repo string, n int, p *Plan) error {
	if p.OutcomeAnnounced && p.StatusCommentID == 0 {
		return nil
	}
	review := "Declared checks passed; intent review is pending."
	if p.Generated != nil {
		review = "Verification passed; intent review is pending."
	}
	if p.IntentConfirmedBy != "" {
		review = "Intent confirmed by @" + p.IntentConfirmedBy + "."
	}
	msg := fmt.Sprintf("### Outcome: %s\n\n[Draft pull request](%s) · [Verification](%s/checks?check_run_id=%d) · [Intent review](%s/checks?check_run_id=%d)\n\n%s Review the proof before merging.", selectedTitle(p), p.PR, p.PR, p.CheckID, p.PR, p.IntentCheckID, review)
	if p.PreviousPR != "" && p.PreviousPR != p.PR {
		label := p.PreviousChoice
		for _, c := range p.Config.Choices {
			if c.ID == p.PreviousChoice {
				label = c.Title
			}
		}
		msg += "\n\n[Earlier outcome: " + label + "](" + p.PreviousPR + ") · intent confirmation superseded."
	}
	if p.StatusCommentID != 0 {
		state := "Ready for intent review"
		if p.IntentConfirmedBy != "" {
			state = "Intent confirmed"
		}
		if err := e.deliveryStatus(ctx, inst, repo, n, p, state, msg); err != nil {
			return err
		}
	} else if err := e.api.Comment(ctx, inst, repo, n, msg); err != nil {
		return err
	}
	p.OutcomeAnnounced = true
	return e.save()
}

// deliveryStatus keeps one GitHub-native status comment for the current plan.
func (e *Engine) deliveryStatus(ctx context.Context, inst int64, repo string, n int, p *Plan, state, body string) error {
	api, ok := e.api.(interface {
		ControlComment(context.Context, int64, string, int, string) (int64, error)
		UpdateComment(context.Context, int64, string, int64, string) error
	})
	if !ok {
		return nil
	}
	msg := "### Trajectory · " + state + "\n\n" + body
	if p.StatusCommentID == 0 {
		id, err := api.ControlComment(ctx, inst, repo, n, msg)
		if err != nil {
			return err
		}
		p.StatusCommentID = id
		return e.save()
	}
	return api.UpdateComment(ctx, inst, repo, p.StatusCommentID, msg)
}
