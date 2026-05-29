package containerdrop

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EnvBrokerSocket is the env var through which a runner tells the drop process
// where the broker's unix socket lives.
const EnvBrokerSocket = "HZ_BROKER_SOCKET"

// SourceFileName is the fixed filename the Transport writes a drop's bundled
// source to inside the per-execution dir (next to the broker socket). Each
// runner hands the drop host this file at the path visible in its own namespace.
const SourceFileName = "drop.js"

// Limits bounds a sandboxed drop. Zero fields are unset. They're enforced by the
// container runtime (DockerRunner → cgroup + ulimit); the process tier can't
// apply them to a foreign `node` child, so it bounds memory via the Node flag
// --max-old-space-size (set by the daemon) and relies on the address-space
// boundary for the rest. Untrusted workloads should use the gVisor tier.
type Limits struct {
	MemoryBytes uint64 // cgroup memory.max (docker --memory)
	CPUSeconds  uint64 // RLIMIT_CPU (docker --ulimit cpu)
	FileBytes   uint64 // RLIMIT_FSIZE (docker --ulimit fsize)
	OpenFiles   uint64 // RLIMIT_NOFILE (docker --ulimit nofile)
}

// dockerArgs renders the limits as `docker run` flags — cgroup + ulimit caps the
// container runtime enforces on the drop. A pids cap is always set as a
// fork-bomb guard.
func (l Limits) dockerArgs() []string {
	a := []string{"--pids-limit", "128"}
	if l.MemoryBytes > 0 {
		a = append(a, "--memory", fmt.Sprintf("%d", l.MemoryBytes))
	}
	if l.CPUSeconds > 0 {
		a = append(a, "--ulimit", fmt.Sprintf("cpu=%d", l.CPUSeconds))
	}
	if l.FileBytes > 0 {
		a = append(a, "--ulimit", fmt.Sprintf("fsize=%d", l.FileBytes))
	}
	if l.OpenFiles > 0 {
		a = append(a, "--ulimit", fmt.Sprintf("nofile=%d", l.OpenFiles))
	}
	return a
}

// ProcessRunner launches the drop host as a plain local subprocess — the
// address-space boundary (a crash/OOM kills the child, not the daemon) but NO
// kernel isolation. It's the dev/default-tier runner; the gVisor DockerRunner is
// the same broker contract with isolation + enforced limits. The socket path is
// handed to the drop via EnvBrokerSocket.
type ProcessRunner struct {
	Stdout, Stderr io.Writer // optional; nil discards
}

func (p ProcessRunner) Run(ctx context.Context, socketPath string, drop DropRef) error {
	if len(drop.Argv) == 0 {
		return fmt.Errorf("ProcessRunner: DropRef.Argv is empty (need a command to launch)")
	}
	argv := append([]string{}, drop.Argv...)
	// Point the drop host at the source the Transport materialized in the socket
	// dir (host path — this runner shares the daemon's filesystem namespace).
	if len(drop.Source) > 0 {
		argv = append(argv, "--source", filepath.Join(filepath.Dir(socketPath), SourceFileName))
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	// Hand the drop a minimal, allowlisted environment — NOT os.Environ().
	// The drop host runs untrusted JS, and the daemon's environment holds the
	// master key (HAZYFLOW_MASTER_KEY), the Postgres DSN, webhook secrets, etc.
	// Inheriting it would let any drop read process.env and exfiltrate every
	// tenant's secrets. Only what `node`/drophost.mjs genuinely need to boot is
	// forwarded; the broker socket is the sole channel back to the daemon. The
	// gVisor tier already runs with a clean env — this brings the process tier
	// in line.
	cmd.Env = sanitizedEnv(socketPath)
	cmd.Stdout = p.Stdout
	// Capture stderr when the caller didn't wire its own, so a drop that crashes
	// before POSTing /result (a syntax error, a thrown top level) reports WHY
	// instead of an opaque "no_result".
	stderr := p.Stderr
	var tail *tailBuffer
	if stderr == nil {
		tail = &tailBuffer{max: stderrTailBytes}
		stderr = tail
	}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if tail != nil {
			if s := tail.String(); s != "" {
				return fmt.Errorf("%w: %s", err, s)
			}
		}
		return err
	}
	return nil
}

// envAllowList is the set of host environment variables forwarded to a
// process-tier drop. Kept deliberately tiny: just what `node` and drophost.mjs
// need to start and behave (executable lookup, temp dir, locale/timezone).
// Anything not listed here — crucially every HAZYFLOW_* secret — is dropped.
var envAllowList = map[string]bool{
	"PATH":   true,
	"HOME":   true,
	"TMPDIR": true,
	"TMP":    true,
	"TEMP":   true,
	"LANG":   true,
	"LC_ALL": true,
	"TZ":     true,
}

// sanitizedEnv builds the drop process environment from an allowlist plus the
// broker socket, instead of inheriting the daemon's full environment. NODE_*
// vars are forwarded (e.g. NODE_OPTIONS, NODE_PATH) because they're the
// daemon's own configuration of the Node host, not attacker-controlled, and the
// host needs them to honor its launch flags.
func sanitizedEnv(socketPath string) []string {
	env := []string{EnvBrokerSocket + "=" + socketPath}
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if envAllowList[k] || strings.HasPrefix(k, "NODE_") {
			env = append(env, kv)
		}
	}
	return env
}

var _ Runner = ProcessRunner{}
