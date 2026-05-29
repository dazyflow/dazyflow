package containerdrop

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"time"
)

// containerWorkDir is where the per-execution dir (broker socket + materialized
// drop source) is bind-mounted; the rest of the rootfs is the read-only image.
const containerWorkDir = "/hz"

// DockerRunner launches the drop in a gVisor (runsc) container — the kernel
// boundary the ProcessRunner lacks. It is the same broker contract as every
// other Runner; only the launch differs:
//
//   - --runtime=runsc      the gVisor sandbox (syscall interception)
//   - --network=none       default-deny egress; the broker socket is the only path out
//   - --read-only          immutable rootfs
//   - --memory/--pids-limit cgroup-enforced HARD caps (the process tier can't do memory)
//
// The broker's unix socket is reached over a bind mount. gVisor gates host unix
// sockets behind runsc's `--host-uds` flag (default: denied), so the runsc
// runtime MUST be configured with at least `--host-uds=open` in the Docker
// daemon's runtimeArgs — otherwise the drop can't dial the broker. See
// DESIGN.md (Stage 2.1) for the daemon.json snippet.
type DockerRunner struct {
	// Image is the rootfs the drop runs on (a stock official node image; the
	// drop host is bind-mounted in via Mounts).
	Image string
	// Command is the in-container entrypoint, e.g. ["node", "/drophost.mjs"].
	Command []string
	// Mounts are host→container read-only bind mounts (e.g. the Node drop host:
	// {".../drophost.mjs": "/drophost.mjs"}). The per-execution dir (broker
	// socket + drop source) is always mounted at /hz separately.
	Mounts map[string]string
	// Runtime is the container runtime; "" → "runsc".
	Runtime string
	Limits  Limits
	Stderr  io.Writer
}

func (d DockerRunner) Run(ctx context.Context, socketPath string, drop DropRef) error {
	if d.Image == "" {
		return fmt.Errorf("DockerRunner: Image is required")
	}
	if len(d.Command) == 0 {
		return fmt.Errorf("DockerRunner: Command is required")
	}
	runtime := d.Runtime
	if runtime == "" {
		runtime = "runsc"
	}
	hostDir := filepath.Dir(socketPath)
	inSock := path.Join(containerWorkDir, filepath.Base(socketPath))

	mounts := map[string]string{}
	for h, c := range d.Mounts {
		mounts[h] = c
	}
	entry := d.Command

	// Name the container after the per-execution dir (unique) so we can force-
	// remove it on return. `docker run --rm` cleans up on a normal exit, but if
	// ctx is cancelled (run budget / shutdown) exec.CommandContext SIGKILLs only
	// the `docker` CLI — the container keeps running. The deferred `rm -f` reaps
	// that orphan; it's a no-op once --rm has done its job.
	name := "hz-drop-" + filepath.Base(hostDir)
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = exec.CommandContext(rmCtx, "docker", "rm", "-f", name).Run()
	}()

	args := []string{
		"run", "--rm",
		"--name", name,
		"--runtime=" + runtime,
		"--network=none",
		"--read-only",
		// gVisor gates connecting to a host unix socket behind --host-uds. We set
		// it per-container via the OCI annotation runsc reads, so the broker
		// socket works without a daemon-wide runtimeArgs change. "open" = connect
		// only (the socket is host-created); the drop never creates host sockets.
		"--annotation", "dev.gvisor.flag.host-uds=open",
		"-v", hostDir + ":" + containerWorkDir,
		"-e", EnvBrokerSocket + "=" + inSock,
	}
	for h, c := range mounts {
		args = append(args, "-v", h+":"+c+":ro")
	}
	args = append(args, d.Limits.dockerArgs()...)
	args = append(args, d.Image)
	args = append(args, entry...)
	if len(drop.Source) > 0 {
		args = append(args, "--source", path.Join(containerWorkDir, SourceFileName))
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	// Capture stderr (docker's + the drop's) when none is wired, so a container
	// that dies before reporting a result explains why instead of "no_result".
	stderr := d.Stderr
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

var _ Runner = DockerRunner{}
