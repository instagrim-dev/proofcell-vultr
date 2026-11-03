package repair

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DockerSandbox executes the model's bounded function and independent tests
// without network, credentials, write access to the mounted source, or host mounts.
type DockerSandbox struct{ WorkRoot, HostWorkRoot, Image string }

func (s DockerSandbox) Verify(ctx context.Context, choice string, source []byte) (Evidence, error) {
	if s.WorkRoot == "" || s.HostWorkRoot == "" || s.Image == "" {
		return Evidence{}, fmt.Errorf("sandbox not configured")
	}
	if choice != "keep" && choice != "end" {
		return Evidence{}, fmt.Errorf("unsupported decision")
	}
	dir, err := os.MkdirTemp(s.WorkRoot, "run-")
	if err != nil {
		return Evidence{}, err
	}
	defer os.RemoveAll(dir)
	if err = os.Chmod(dir, 0755); err != nil {
		return Evidence{}, err
	}
	tests := testsFor(choice)
	for name, data := range map[string][]byte{"access.go": source, "access_test.go": tests, "go.mod": []byte("module candidate\n\ngo 1.22\n")} {
		if err = os.WriteFile(filepath.Join(dir, name), data, 0644); err != nil {
			return Evidence{}, err
		}
	}
	random := make([]byte, 8)
	if _, err = rand.Read(random); err != nil {
		return Evidence{}, err
	}
	tag := hex.EncodeToString(random)
	canary := filepath.Join(s.WorkRoot, "canary-"+tag)
	if err = os.WriteFile(canary, []byte("Trajectory host canary\n"), 0600); err != nil {
		return Evidence{}, err
	}
	defer os.Remove(canary)
	before, err := os.ReadFile(canary)
	if err != nil {
		return Evidence{}, err
	}
	name := "trajectory-repair-" + tag
	hostDir := filepath.Join(s.HostWorkRoot, filepath.Base(dir))
	hostCanary := filepath.Join(s.HostWorkRoot, filepath.Base(canary))
	script := `if [ -e "$1" ]; then echo HOST_PATH_VISIBLE; exit 4; fi; echo HOST_PATH_NOT_VISIBLE; go test -v .; result=$?; rm -f -- "$1"; echo SANDBOX_DELETE_ATTEMPT_FINISHED; exit "$result"`
	args := []string{"run", "--rm", "--pull=never", "--name", name, "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=128", "--memory=768m", "--cpus=0.8", "--user=65534:65534", "--tmpfs", "/tmp:rw,exec,nosuid,size=512m", "--mount", "type=bind,src=" + hostDir + ",dst=/work,readonly", "-w", "/work", "-e", "HOME=/tmp", "-e", "GOCACHE=/tmp/cache", "-e", "GOMODCACHE=/tmp/mod", "-e", "GOPROXY=off", s.Image, "sh", "-c", script, "runner", hostCanary}
	runCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	out, runErr := exec.CommandContext(runCtx, "docker", args...).CombinedOutput()
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cleanupCancel()
	_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", name).Run()
	remaining, listErr := exec.CommandContext(cleanupCtx, "docker", "ps", "-a", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}").Output()
	after, readErr := os.ReadFile(canary)
	ev := Evidence{TestsSHA256: digest(tests), Output: string(out), CanaryPreserved: readErr == nil && digest(before) == digest(after), CleanupConfirmed: listErr == nil && strings.TrimSpace(string(remaining)) == ""}
	if len(ev.Output) > 12000 {
		ev.Output = ev.Output[:12000]
	}
	if runErr != nil || !ev.CanaryPreserved || !ev.CleanupConfirmed || !strings.Contains(ev.Output, "HOST_PATH_NOT_VISIBLE\n") || !strings.Contains(ev.Output, "SANDBOX_DELETE_ATTEMPT_FINISHED\n") {
		return ev, fmt.Errorf("sandbox verification failed")
	}
	return ev, nil
}
func testsFor(choice string) []byte {
	expected := "true"
	if choice == "end" {
		expected = "false"
	}
	return []byte(fmt.Sprintf(`package subscription
import "testing"
func TestBoundary(t *testing.T) {
 for _, tc := range []struct{ name string; paidUntil, now int64; want bool }{
 {"before expiry",30,29,%s}, {"at expiry",30,30,false}, {"after expiry",30,31,false}, {"different expiry",18,17,%s},
 } { t.Run(tc.name,func(t *testing.T){if got:=HasAccess(tc.paidUntil,tc.now);got!=tc.want{t.Fatalf("got %%v, want %%v",got,tc.want)}}) }
}
`, expected, expected))
}
