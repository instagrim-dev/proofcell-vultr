// Package verifier checks the observations made by a fixed sandbox fixture.
package verifier

import "strings"

// Verify confirms this one fixture's host canary, visibility marker and cleanup.
// It is not a general proof that a container is secure.
func Verify(before, after, output string, cleaned bool) bool {
	if before == "" || before != after || !cleaned {
		return false
	}
	for _, line := range strings.Split(output, "\n") {
		if line == "HOST_PATH_NOT_VISIBLE" {
			return true
		}
	}
	return false
}
