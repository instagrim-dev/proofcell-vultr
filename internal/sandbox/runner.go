// Package sandbox runs a fixed local containment fixture in Docker.
package sandbox

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/instagrim-dev/proofcell-vultr/internal/verifier"
)

const defaultImage = "busybox@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"

const fixture = `if [ -e "$1" ]; then printf 'HOST_PATH_VISIBLE\n'; exit 2; fi
printf 'HOST_PATH_NOT_VISIBLE\n'
rm -f -- "$1"
printf 'SANDBOX_DELETE_ATTEMPT_FINISHED\n'`

type Report struct {
	Outcome               string `json:"outcome"`
	SandboxOutput         string `json:"sandbox_output"`
	HostCanaryPreserved   bool   `json:"host_canary_preserved"`
	SandboxHostPathAbsent bool   `json:"sandbox_host_path_absent"`
	CleanupConfirmed      bool   `json:"cleanup_confirmed"`
	Verified              bool   `json:"verified"`
	Image                 string `json:"image"`
}

type Runner struct{ Image string }

func dockerArgs(image, name, canary string) []string {
	return []string{"run", "--rm", "--pull=never", "--name", name, "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=64", "--memory=128m", "--cpus=0.5", "--user=65534:65534", "--tmpfs", "/tmp:rw,noexec,nosuid,size=16m", image, "sh", "-c", fixture, "proofcell", canary}
}

// Run runs only the built-in fixture. No user-supplied code enters the runner.
func (r Runner) Run(ctx context.Context) (Report, error) {
	image := r.Image
	if image == "" {
		image = defaultImage
	}
	report := Report{Outcome: "failed", Image: image}
	dir, err := os.MkdirTemp("", "proofcell-canary-")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(dir)
	canary := filepath.Join(dir, "canary.txt")
	if err := os.WriteFile(canary, []byte("ProofCell host canary\n"), 0600); err != nil {
		return report, err
	}
	before, err := os.ReadFile(canary)
	if err != nil {
		return report, err
	}
	nameBytes := make([]byte, 8)
	if _, err := rand.Read(nameBytes); err != nil {
		return report, err
	}
	name := "proofcell-fixture-" + hex.EncodeToString(nameBytes)
	runCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	output, runErr := exec.CommandContext(runCtx, "docker", dockerArgs(image, name, canary)...).CombinedOutput()
	report.SandboxOutput = string(output)
	// Clean up by the randomly generated name even if the CLI timed out.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cleanupCancel()
	_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", name).Run()
	list, listErr := exec.CommandContext(cleanupCtx, "docker", "ps", "-a", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}").Output()
	report.CleanupConfirmed = listErr == nil && strings.TrimSpace(string(list)) == ""
	after, readErr := os.ReadFile(canary)
	beforeHash, afterHash := sha256.Sum256(before), sha256.Sum256(after)
	report.HostCanaryPreserved = readErr == nil && beforeHash == afterHash
	report.SandboxHostPathAbsent = strings.Contains("\n"+report.SandboxOutput, "\nHOST_PATH_NOT_VISIBLE\n")
	report.Verified = runErr == nil && readErr == nil && verifier.Verify(hex.EncodeToString(beforeHash[:]), hex.EncodeToString(afterHash[:]), report.SandboxOutput, report.CleanupConfirmed)
	if runErr != nil {
		return report, fmt.Errorf("Docker fixture failed: %w", runErr)
	}
	if !report.Verified {
		return report, fmt.Errorf("fixture observations did not verify")
	}
	report.Outcome = "verified"
	return report, nil
}
