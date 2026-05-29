// Command manifestgen produces officialdrops/manifests.json: for every embedded
// *.ts drop, it transpiles the source to ESM and runs the SAME Node drop host
// (`drophost.mjs --emit-manifest`) the daemon uses to read a manifest, capturing
// the raw emitted JSON. Embedding these at generate time means boot registers
// official drops with AddPrebuilt — no Node spawn per drop on the hot path.
//
// Run via `go generate ./officialdrops` (which sets cwd to the officialdrops
// package dir). Requires `node` on PATH. Regenerate after editing any drop body
// or the manifest of any official drop, else the embedded manifest goes stale
// (register_test.go guards count parity, but not field-level drift).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/engine/jsdrop"
)

// drophost is relative to the officialdrops package dir (go generate's cwd).
const drophost = "../engine/containerdrop/nodehost/drophost.mjs"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "manifestgen:", err)
		os.Exit(1)
	}
}

func run() error {
	node, err := exec.LookPath("node")
	if err != nil {
		return fmt.Errorf("node is required to generate manifests: %w", err)
	}
	if _, err := os.Stat(drophost); err != nil {
		return fmt.Errorf("drop host not found at %s (run via `go generate ./officialdrops`): %w", drophost, err)
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		return err
	}
	manifests := map[string]json.RawMessage{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".ts") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		raw, err := emitManifest(node, name, string(src))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		manifests[name] = raw
	}

	// Marshal with sorted keys for a stable diff.
	keys := make([]string, 0, len(manifests))
	for k := range manifests {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf strings.Builder
	buf.WriteString("{\n")
	for i, k := range keys {
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteString(": ")
		buf.Write(manifests[k])
		if i < len(keys)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("}\n")

	if err := os.WriteFile("manifests.json", []byte(buf.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("manifestgen: wrote manifests.json (%d drops)\n", len(manifests))
	return nil
}

func emitManifest(node, name, source string) (json.RawMessage, error) {
	esm, err := jsdrop.TranspileESM(source)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "hz-manifestgen-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	srcPath := filepath.Join(dir, "drop.js")
	if err := os.WriteFile(srcPath, []byte(esm), 0o600); err != nil {
		return nil, err
	}
	cmd := exec.Command(node, drophost, "--emit-manifest", "--source", srcPath)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s", msg)
	}
	// Validate it parses as a manifest, but embed the raw emitted JSON so the
	// boot path (jsdrop.ParseManifest) sees exactly what the runtime would.
	if _, err := jsdrop.ParseManifest(out); err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}
