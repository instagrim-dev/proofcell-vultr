package githubapp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// nativeAPI is implemented by the GitHub client. Control IDs bind interactive
// edits to comments this installation actually created.
type nativeAPI interface {
	ControlComment(context.Context, int64, string, int, string) (int64, error)
	PRHead(context.Context, int64, string, string) (string, error)
	CreateDecisionCheck(context.Context, int64, string, string, string, string) (int64, error)
	UpdateDecisionCheck(context.Context, int64, string, int64, string, string) error
	VerifyPRPatch(context.Context, int64, string, string, string, map[string][]byte) error
}

func checkSummary(p *Plan) string {
	base := "Repository-defined preimage and JSON checks passed."
	if p.Generated == nil {
		return base + " Behavioral sandbox checks are not connected."
	}
	return fmt.Sprintf("%s Sandbox boundary tests passed for candidate SHA-256 `%s`; test SHA-256 `%s`. Vultr model `%s`, response `%s`. Host canary preserved and container removed. Revalidate rechecks inputs and PR tree; it does not rerun sandbox tests.", base, p.Generated.SourceSHA256, p.Generated.Evidence.TestsSHA256, p.Generated.Model, p.Generated.RequestID)
}

func (e *Engine) ensureCheck(ctx context.Context, inst int64, repo string, p *Plan) error {
	api, ok := e.api.(nativeAPI)
	if !ok || p.CheckID != 0 {
		return nil
	}
	head, err := api.PRHead(ctx, inst, repo, p.PR)
	if err != nil {
		return err
	}
	if err := api.VerifyPRPatch(ctx, inst, repo, p.BaseSHA, head, p.Files); err != nil {
		return err
	}
	id, err := api.CreateDecisionCheck(ctx, inst, repo, head, p.PatchHash, checkSummary(p))
	if err != nil {
		return err
	}
	if id <= 0 {
		return fmt.Errorf("GitHub returned an invalid check ID")
	}
	p.CheckID, p.CheckHead = id, head
	return e.save()
}

func (e *Engine) checkIssue(ev Event) int {
	prefix := fmt.Sprintf("%d/%s/", ev.Installation.ID, ev.Repository.FullName)
	for key, p := range e.data.Plans {
		if p.CheckID > 0 && p.CheckID == ev.CheckRun.ID && strings.HasPrefix(key, prefix) {
			n, _ := strconv.Atoi(strings.TrimPrefix(key, prefix))
			return n
		}
	}
	return 0
}

func (e *Engine) processCheck(ctx context.Context, ev Event) error {
	inst, repo := ev.Installation.ID, ev.Repository.FullName
	prefix := fmt.Sprintf("%d/%s/", inst, repo)
	var p *Plan
	var n int
	for key, candidate := range e.data.Plans {
		if strings.HasPrefix(key, prefix) && candidate.CheckID == ev.CheckRun.ID && candidate.CheckID > 0 {
			p = candidate
			n, _ = strconv.Atoi(strings.TrimPrefix(key, prefix))
			break
		}
	}
	if p == nil || n <= 0 || p.PR == "" || ev.CheckRun.HeadSHA != p.CheckHead {
		return nil
	}
	allowed, err := e.api.CanWrite(ctx, inst, repo, ev.Sender.Login)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("action requires current repository write permission")
	}
	api, ok := e.api.(nativeAPI)
	if !ok {
		return fmt.Errorf("native checks unavailable")
	}
	head, err := api.PRHead(ctx, inst, repo, p.PR)
	if err != nil {
		return err
	}
	if head != p.CheckHead {
		return fmt.Errorf("pull request head changed; create a new decision")
	}
	switch ev.RequestedAction.Identifier {
	case "show_decision":
		return e.api.Comment(ctx, inst, repo, n, fmt.Sprintf("### Decision receipt\nChoice: `%s`\nPlan: `%s`\nPatch: `%s`\nDraft PR: %s", p.Choice, p.ID, p.PatchHash, p.PR))
	case "revalidate":
		title, body, err := e.api.Issue(ctx, inst, repo, n)
		if err != nil {
			return err
		}
		_, base, err := e.api.Repository(ctx, inst, repo)
		if err != nil {
			return err
		}
		if issueHash(title, body) != p.IssueDigest || base != p.BaseSHA {
			return fmt.Errorf("issue or base changed; create a new decision")
		}
		encoded, _ := json.Marshal(p.Files)
		if hash(encoded) != p.PatchHash {
			return fmt.Errorf("patch bytes changed")
		}
		var choice *Choice
		for i := range p.Config.Choices {
			if p.Config.Choices[i].ID == p.Choice {
				choice = &p.Config.Choices[i]
			}
		}
		if choice == nil {
			return fmt.Errorf("decision missing")
		}
		for _, change := range choice.Changes {
			old, err := e.api.File(ctx, inst, repo, change.Path, base)
			if err != nil {
				return err
			}
			contentMatches := string(p.Files[change.Path]) == change.Content
			if change.Path == "access.go" && p.Generated != nil {
				contentMatches = hash(p.Files[change.Path]) == p.Generated.SourceSHA256 && string(p.Files[change.Path]) == string(p.Generated.Source)
			}
			if hash(old) != change.Before || !contentMatches {
				return fmt.Errorf("patch preimage or content changed")
			}
		}
		for _, check := range choice.Checks {
			var doc map[string]any
			if json.Unmarshal(p.Files[check.Path], &doc) != nil {
				return fmt.Errorf("invalid JSON")
			}
			got, found := doc[check.Key]
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(check.Equals)
			if !found || string(a) != string(b) {
				return fmt.Errorf("declared JSON check failed")
			}
		}
		if err := api.VerifyPRPatch(ctx, inst, repo, p.BaseSHA, head, p.Files); err != nil {
			return err
		}
		return api.UpdateDecisionCheck(ctx, inst, repo, p.CheckID, p.PatchHash, checkSummary(p))
	default:
		return nil
	}
}
