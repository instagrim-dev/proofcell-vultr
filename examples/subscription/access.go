package subscription

// HasAccess is the current immediate-revocation behavior.
func HasAccess(paidUntil, now int64) bool { return false }
