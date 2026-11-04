# Decision cards and verification summaries

An optional `description` on each choice provides a short prospective consequence beneath its GitHub task-list checkbox. Keep descriptions static: the complete comment body is saved and matched when a choice changes. Select one checkbox; to change a choice, clear it before selecting another. Clearing the choice invalidates intent confirmation. Progress is published separately.

The `vultr-go-access-v1` example supplies default descriptions only when its configured patch and checks support the relevant values. The access-boundary summary requires the recognized independent test-suite hash and four captured passing subtests. Unrecognized or incomplete receipts cannot yield a successful Verification check. The full test output remains in the Check.

For the refund example, an independent integer calculation checks that the proposed amount is exactly `paid_cents × unused_days / period_days`. The example does not execute a refund. Non-integral amounts are rejected because no rounding policy is defined; the check does not claim to test rounding.
