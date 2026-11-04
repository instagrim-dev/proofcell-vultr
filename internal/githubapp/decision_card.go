package githubapp

import "fmt"

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
			switch c.ID {
			case "keep":
				description = "Would keep access until the paid period ends and stop renewal."
			case "end":
				description = "Would end access when the customer cancels."
			case "refund":
				description = "Would end access immediately and record a proposed prorated refund for unused time."
			}
		}
		if description != "" {
			msg += "\n  " + description + "\n\n"
		}
	}
	return msg
}
