# Decision-guided GitHub delivery

The issue holds the intent and the selected outcome. In `delivery: on-decision` mode, selecting an outcome synthesizes and verifies its patch, then automatically creates the immutable branch and draft PR. The issue receives a short outcome link. Diffs live in Files changed; receipts, test output and provenance live in Checks.

Two Checks bind the current PR head to the issue digest and selected outcome: `Trajectory / Verification` reports the bounded checks and source/test/model evidence; `Trajectory / Intent` requests a repository writer to confirm that the evidence resolves the stated intent. Confirmation re-reads the issue, base, current decision and PR tree. It records reviewer and exact head. Merge remains GitHub's native review/merge operation after this link is made. Required status checks can enforce that order on the demo repository.

Changing a selection invalidates the earlier intent approval and creates a distinct draft with fresh checks. The earlier draft stays inspectable. Edited issue or PR inputs invalidate intent confirmation. Existing v1 plans without the delivery setting retain their legacy publication controls so stored runs remain recoverable.

Acceptance: native choice creates draft without a second publication checkbox; issue comments contain no raw receipt dumps; Checks expose intent, decision, scope, full output and hashes; confirmation cannot accept stale issue/base/selection/head; re-decision cancels prior intent status; test failure cannot produce a successful Verification check; GitHub CI remains cost-free (App Checks, no Actions run).
