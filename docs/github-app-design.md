# Trajectory GitHub App design

The issue is the decision surface. `/trajectory plan` loads a bounded question and transformations from `.trajectory.json` at the repository default-branch SHA. `/trajectory choose <id>` records an authorized writer's answer and validates the selected patch against exact file preimages and repository-defined JSON expectations. `/trajectory publish` creates an app-owned branch and draft PR with the decision and validation receipt. New plans supersede earlier choices; existing PRs remain historical artifacts.

All mutating commands require the sender's current repository permission via GitHub API. Verified signatures precede durable enqueue. A single worker processes persisted deliveries; failed attempts stay inspectable and duplicate deliveries do not repeat completed commands. State is namespaced by installation/repository/issue. Issue title/body and base SHA must remain current before selection/publication. Publication uses an atomic Git tree commit and deterministic app branch; retries reconcile branch and PR rather than force-push.

Permissions: metadata read; contents, issues and pull requests write. No Actions or workflow writes. The App JWT is used only to mint short-lived repository-scoped installation tokens. Secrets stay outside config, logs and receipts. The setup page posts a GitHub App manifest to GitHub; registration and installation happen in GitHub's own consent UI.

Initial transformations are repository-authored file replacements with SHA-256 preimages and explicit JSON expectations. The app checks them without running repository code. It reports these as bounded structural/declared checks, not arbitrary behavioral verification or model synthesis. Vultr inference and disposable candidate execution plug into this boundary later. Existing Meta artifacts remain private.

Implementation: GitHub API/auth/publication client; durable issue engine/webhook; standalone server/setup manifest; fixture config and end-to-end simulated GitHub tests. Local checks only; GitHub CI remains manual-only under the user's cost policy.

## Quorum refinements and implemented boundary

The design quorum required semantic issue hashes (title and body), exact diff review, current write permission, deterministic branch/PR reconciliation, and revision history. Implementation review additionally found out-of-order deliveries could restore older decisions. The worker now orders persisted comment IDs and retains a last-applied watermark. It preserves selection when the same plan is requested again. Publication rechecks issue inputs at each GitHub write boundary. Previously published work can be recovered read-only after inputs change. Initial queue-directory durability includes syncing its parent.

The current bounded slice has one consequential question per plan. Owners can revise its selected choice, see a different patch, and publish that exact revision; sequential dependent questions and general synthesis remain extension points. Three terminal delivery attempts leave their error on disk and attempt an issue explanation; a new command creates a fresh delivery. The App creates draft PRs and never merges them.
