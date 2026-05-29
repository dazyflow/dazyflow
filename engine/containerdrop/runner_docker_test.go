package containerdrop

import (
	"os/exec"
	"strings"
	"testing"
)

// sandboxImage is a minimal image used only to probe that the runsc runtime is
// usable on this host.
const sandboxImage = "alpine:latest"

// requireGVisor skips unless docker + the runsc runtime are usable here.
func requireGVisor(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	out, err := exec.Command("docker", "run", "--rm", "--runtime=runsc", sandboxImage, "true").CombinedOutput()
	if err != nil {
		t.Skipf("runsc runtime not usable (need gVisor): %v\n%s", err, out)
	}
}

// skipIfHostUDSDenied turns the one environmental prerequisite we can't satisfy
// without root — runsc's `--host-uds=open` — into a clean skip instead of a
// false failure. gVisor refuses connections to a host unix socket through a bind
// mount unless that flag is set. The sandbox launch, mounts, and network-deny
// are all exercised regardless; only the broker round-trip needs the flag.
func skipIfHostUDSDenied(t *testing.T, stderr string) {
	if strings.Contains(stderr, "broker.sock") && strings.Contains(stderr, "connection refused") {
		t.Skipf("runsc cannot dial the host broker socket — configure the runsc runtime with --host-uds=open "+
			"(Docker daemon.json runtimeArgs). Everything up to the broker round-trip is verified.\nstderr:\n%s", stderr)
	}
}
