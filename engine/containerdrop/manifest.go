package containerdrop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine/jsdrop"
)

// manifestExtractTimeout bounds the throwaway Node process that reads a drop's
// manifest — generous for a cold `node` start, tight enough that a hostile or
// looping module top level can't hang an install.
const manifestExtractTimeout = 30 * time.Second

// NodeManifestExtractor returns a jsdrop.Catalog Extract hook that reads a
// drop's manifest by running `drophost.mjs --emit-manifest` in a short-lived
// Node process. This is the goja-free replacement for in-process manifest
// reading: the ONLY component that reads a drop's manifest is the same Node
// runtime that will execute it, so what the daemon gates on is exactly what
// will run. The drop's module top level executes here (its `manifest` is a
// static object), but with no broker socket — it has no capabilities.
func NodeManifestExtractor(node, drophost string) func(name, source string) (core.Manifest, error) {
	return func(name, source string) (core.Manifest, error) {
		ctx, cancel := context.WithTimeout(context.Background(), manifestExtractTimeout)
		defer cancel()
		return extractManifest(ctx, node, drophost, name, source)
	}
}

func extractManifest(ctx context.Context, node, drophost, name, source string) (core.Manifest, error) {
	esm, err := jsdrop.TranspileESM(source)
	if err != nil {
		return core.Manifest{}, err
	}
	dir, err := os.MkdirTemp("", "hz-manifest-")
	if err != nil {
		return core.Manifest{}, err
	}
	defer os.RemoveAll(dir)
	src := filepath.Join(dir, SourceFileName)
	if err := os.WriteFile(src, []byte(esm), 0o600); err != nil {
		return core.Manifest{}, err
	}

	cmd := exec.CommandContext(ctx, node, drophost, "--emit-manifest", "--source", src)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return core.Manifest{}, fmt.Errorf("read manifest for %q: %s", name, msg)
	}
	return jsdrop.ParseManifest(out)
}
