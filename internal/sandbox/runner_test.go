package sandbox

import (
	"slices"
	"testing"
)

func TestBoundaryRunHasIsolationAndNoMounts(t *testing.T) {
	args := dockerArgs("fixture-image", "run-123", "/tmp/proofcell-test/canary")
	for _, required := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=64", "--memory=128m", "--cpus=0.5", "--user=65534:65534", "--rm"} {
		if !slices.Contains(args, required) {
			t.Errorf("missing %s", required)
		}
	}
	for _, arg := range args {
		if arg == "-v" || arg == "--volume" || arg == "--privileged" {
			t.Errorf("unsafe argument %q", arg)
		}
	}
}
