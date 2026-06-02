package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine/containerdrop"
	"git.sr.ht/~klahr/hazyflow/engine/jsdrop"
)

// configureScriptedRuntime wires the catalog's Run and Extract hooks so EVERY
// scripted drop (official + runtime-installed) is read and executed by ONE
// runtime: the Node drop host (drophost.mjs), under resource limits, reaching
// the daemon only through the broker. There is no in-process JS engine — Node is
// mandatory; if it or drophost.mjs is missing the daemon refuses to boot rather
// than silently serving a catalog it can't execute. Controlled by:
//
//	HAZYFLOW_SANDBOX_RUNTIME          process (default) | gvisor — isolation tier
//	HAZYFLOW_NODE_DROPHOST            path to drophost.mjs (else: next to hzd)
//	HAZYFLOW_SANDBOX_IMAGE            node image for the gvisor tier (default node:22-alpine)
//	HAZYFLOW_SANDBOX_MEMORY_BYTES     memory cap (default 256 MiB) — gVisor: cgroup
//	                                  --memory; process: node --max-old-space-size
//	HAZYFLOW_SANDBOX_CPU_SECONDS      CPU-time cap (default 30s) — gVisor --ulimit cpu
//	HAZYFLOW_SANDBOX_FILE_BYTES       max file size (default 64 MiB) — gVisor --ulimit fsize
//	HAZYFLOW_SANDBOX_OPEN_FILES       max open files (default 256) — gVisor --ulimit nofile
//
// CPU/file/fd caps are enforced only on the gVisor tier (docker ulimits); the
// process tier bounds memory via --max-old-space-size and relies on the
// address-space boundary otherwise.
func configureScriptedRuntime(cat *jsdrop.Catalog, http jsdrop.HTTPDoer, tokens jsdrop.TokenResolver, reserve jsdrop.QuotaReserve) {
	mjs, ok := resolveNodeDropHost()
	if !ok {
		log.Fatalf("the Node drop host (drophost.mjs) was not found; set HAZYFLOW_NODE_DROPHOST or place it next to hzd")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		log.Fatalf("scripted drops need `node` on PATH: %v", err)
	}
	limits := containerdrop.Limits{
		MemoryBytes: envUint("HAZYFLOW_SANDBOX_MEMORY_BYTES", 256<<20),
		CPUSeconds:  envUint("HAZYFLOW_SANDBOX_CPU_SECONDS", 30),
		FileBytes:   envUint("HAZYFLOW_SANDBOX_FILE_BYTES", 64<<20),
		OpenFiles:   envUint("HAZYFLOW_SANDBOX_OPEN_FILES", 256),
	}
	host := containerdrop.Host{
		HTTP:  http,
		Token: tokens,
		Files: func(job core.Job) jsdrop.FileStore { return jsdrop.NewJobFileStore(job, reserve) },
	}

	// Isolation tier: "gvisor" runs the Node host under runsc (kernel boundary,
	// cgroup-hard limits); "process" is address-space only (a crash/OOM can't
	// take the daemon down). In process mode, bound the Node heap via
	// --max-old-space-size (GOMEMLIMIT/rlimit env don't apply to a Node child);
	// the hard caps belong to the gVisor tier.
	var runner containerdrop.Runner
	argv := []string{node}
	if mb := limits.MemoryBytes >> 20; mb > 0 {
		argv = append(argv, fmt.Sprintf("--max-old-space-size=%d", mb))
	}
	argv = append(argv, mjs)
	switch strings.ToLower(envStr("HAZYFLOW_SANDBOX_RUNTIME", "process")) {
	case "gvisor", "runsc":
		image := envStr("HAZYFLOW_SANDBOX_IMAGE", "node:22-alpine")
		runner = containerdrop.DockerRunner{
			Image:   image,
			Command: []string{"node", "/drophost.mjs"},
			Mounts:  map[string]string{mjs: "/drophost.mjs"},
			Runtime: "runsc",
			Limits:  limits,
		}
		log.Printf("scripted runtime: Node in gVisor (runsc), image=%s, drophost=%s", image, mjs)
	default:
		runner = containerdrop.ProcessRunner{}
		log.Printf("scripted runtime: Node process tier (no kernel isolation), node=%s drophost=%s", node, mjs)
	}

	// Hard per-execution wall-clock ceiling — the backstop against a runaway or
	// never-settling drop pinning a worker. 0 → containerdrop.DefaultMaxRunDuration;
	// a sooner per-node TimeoutSeconds still wins.
	maxRun := envDuration("HAZYFLOW_SCRIPTED_DROP_TIMEOUT", 0)
	cat.Run = func(m core.Manifest, jsESM string, trusted bool) core.Transport {
		// Egress policy is tier-aware. A drop that declares `egress` is always
		// locked to that allowlist. For one that declares none: a trusted
		// (official/verified) drop falls back to the daemon-global SSRF guard +
		// egress policy (the behavior first-party drops had in-process), while an
		// untrusted (community) drop is restricted so an empty allowlist denies
		// all fetch — it must declare its egress to reach the network at all.
		// This bounds exfiltration by unvetted marketplace code.
		restrictEgress := !trusted || len(m.Egress) > 0
		tr := containerdrop.NewTransport(
			m,
			containerdrop.DropRef{ID: m.ID, Argv: argv, Source: []byte(jsESM), RestrictEgress: restrictEgress, Egress: m.Egress},
			runner,
			host,
		)
		tr.MaxRunDuration = maxRun
		return tr
	}
	// Manifest reading uses the same Node runtime that executes drops — a drop's
	// gated capabilities are exactly what `drophost.mjs` will run.
	cat.Extract = containerdrop.NodeManifestExtractor(node, mjs)
}

// resolveNodeDropHost finds drophost.mjs: an explicit env path, else next to the
// running hzd executable.
func resolveNodeDropHost() (string, bool) {
	if p := os.Getenv("HAZYFLOW_NODE_DROPHOST"); p != "" {
		return p, fileExists(p)
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "drophost.mjs")
		if fileExists(cand) {
			return cand, true
		}
	}
	return "", false
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func envUint(key string, def uint64) uint64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		log.Printf("%s=%q is not a non-negative integer; using default %d", key, v, def)
		return def
	}
	return n
}
