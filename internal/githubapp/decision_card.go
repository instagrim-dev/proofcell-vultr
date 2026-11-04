package githubapp

import (
	"encoding/json"
	"fmt"
)

// defaultAccessDescription only describes values present in both the patch
// and the repository's declared checks; IDs alone do not establish semantics.
func defaultAccessDescription(c Choice) string {
	expected := map[string]any{"renewal_enabled": false}
	switch c.ID {
	case "keep":
		expected["access_granted"] = true
		expected["access_ends"] = "paid_period_end"
	case "end":
		expected["access_granted"] = false
		expected["access_ends"] = "immediately"
	case "refund":
		expected["access_granted"] = false
		expected["access_ends"] = "immediately"
		expected["refund_status"] = "proposed"
	default:
		return ""
	}
	var doc map[string]any
	for _, ch := range c.Changes {
		if ch.Path == "subscription.json" {
			if json.Unmarshal([]byte(ch.Content), &doc) != nil {
				return ""
			}
			if c.ID == "refund" && verifyExactProration([]byte(ch.Content)) != nil {
				return ""
			}
			break
		}
	}
	for key, want := range expected {
		a, _ := json.Marshal(doc[key])
		b, _ := json.Marshal(want)
		if string(a) != string(b) {
			return ""
		}
		found := false
		for _, check := range c.Checks {
			v, _ := json.Marshal(check.Equals)
			if check.Path == "subscription.json" && check.Key == key && string(v) == string(b) {
				found = true
				break
			}
		}
		if !found {
			return ""
		}
	}
	switch c.ID {
	case "keep":
		return "Would keep access until the paid period ends and stop renewal."
	case "end":
		return "Would end access when the customer cancels."
	case "refund":
		return "Would end access immediately and record a proposed prorated refund for unused time."
	}
	return ""
}

func decisionComment(id string, cfg Config) string {
	msg := "### Trajectory decision `" + id + "`\n" + cfg.Question + "\n\nSelect one outcome."
	if cfg.Delivery == "on-decision" {
		msg += " Trajectory prepares a draft pull request for your choice. To change it, clear the selected box first, then select another. Clearing a selection invalidates its intent confirmation."
	}
	msg += "\n\n"
	for _, c := range cfg.Choices {
		msg += fmt.Sprintf("- [ ] %s <!-- %s -->\n", c.Title, c.ID)
		description := c.Description
		if description == "" && cfg.Generation == "vultr-go-access-v1" {
			description = defaultAccessDescription(c)
		}
		if description != "" {
			msg += "\n  " + description + "\n\n"
		}
	}
	return msg
}
