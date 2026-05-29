package jsdrop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// TokenResolver returns a current OAuth access token for (tenant-from-ctx,
// provider, account). Refresh happens inside the resolver, so callers always
// get a usable token. The daemon wires this to its OAuth registry.
type TokenResolver func(ctx context.Context, provider, account string) (string, error)

// QuotaReserve atomically reserves n bytes against a tenant's budget for the
// duration of a files.write, returning a release to free the reservation (it
// closes the concurrent-write race the snapshot check can't). Returns
// core.ErrQuotaExceeded when the write wouldn't fit. The daemon wires this to
// the same FSQuota.Reserve the file_write drop uses. Nil → snapshot-only.
type QuotaReserve func(tenant string, n int64) (release func(), err error)

// jobFiles backs ctx.files for one job, confining every path to the job's
// workspace/scratch roots via os.Root. Paths are bare (relative to the
// persistent workspace) or "scratch://…" (per-run) — the same convention the
// shared sandbox helper uses, so a scripted drop's files interoperate with
// native file_read/file_write. os.Root refuses traversal at the OS level.
//
// NOTE: engine/jsdrop sits outside the integrations/ tree, so it can't import
// integrations/internal/sandbox. ResolveRoot below mirrors sandbox.Resolve
// exactly; keep the two in sync.
//
// NewJobFileStore returns the sandboxed FileStore the broker Host wires for a
// single job, so a sandboxed drop's ctx.files is confined to the workspace/
// scratch roots with the same quota discipline as the native file drops.
func NewJobFileStore(job core.Job, reserve QuotaReserve) FileStore {
	return &jobFiles{job: job, reserve: reserve}
}

type jobFiles struct {
	job     core.Job
	reserve QuotaReserve // nil → snapshot-only quota check
}

func (f *jobFiles) open(p string) (*os.Root, string, error) {
	dir, rel, err := ResolveRoot(f.job, p)
	if err != nil {
		return nil, "", err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", fmt.Errorf("open sandbox root: %w", err)
	}
	return root, rel, nil
}

func (f *jobFiles) Read(p string) ([]byte, error) {
	root, rel, err := f.open(p)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(rel)
	if err != nil {
		return nil, escapeErr(p, err)
	}
	defer file.Close()
	return io.ReadAll(file)
}

func (f *jobFiles) Exists(p string) (bool, error) {
	root, rel, err := f.open(p)
	if err != nil {
		return false, err
	}
	defer root.Close()
	if _, err := root.Stat(rel); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, escapeErr(p, err)
	}
	return true, nil
}

func (f *jobFiles) Write(p string, data []byte) error {
	n := int64(len(data))
	// file_write's exact two-step: a fast snapshot gate, then the atomic
	// reserve-and-hold that closes the concurrent-write race (two writes
	// from one tenant each passing the stale snapshot and together busting
	// the limit). The reservation is held until the write completes.
	if f.job.QuotaLimit > 0 && f.job.QuotaUsed+n > f.job.QuotaLimit {
		return fmt.Errorf("quota_exceeded: write of %d bytes would push tenant past %d (currently %d)",
			n, f.job.QuotaLimit, f.job.QuotaUsed)
	}
	if f.reserve != nil {
		release, err := f.reserve(f.job.Tenant, n)
		if err != nil {
			if errors.Is(err, core.ErrQuotaExceeded) {
				return fmt.Errorf("quota_exceeded: write of %d bytes would exceed tenant quota", n)
			}
			return fmt.Errorf("reserve quota: %w", err)
		}
		defer release()
	}
	root, rel, err := f.open(p)
	if err != nil {
		return err
	}
	defer root.Close()
	out, err := root.Create(rel)
	if err != nil {
		return escapeErr(p, err)
	}
	defer out.Close()
	_, err = out.Write(data)
	return err
}

// ResolveRoot mirrors integrations/internal/sandbox.Resolve: a "scratch://"
// prefix routes to the ephemeral per-run root; anything else is relative to
// the persistent workspace root. engine/jsdrop sits outside the integrations/
// tree and can't import that internal package, so this is a hand-kept copy —
// exported so a parity test under integrations/internal/sandbox can assert the
// two stay byte-for-byte identical (see that package's jsdrop_parity_test.go).
func ResolveRoot(job core.Job, p string) (dir, rel string, err error) {
	if rest, ok := strings.CutPrefix(p, "scratch://"); ok {
		if job.ScratchRoot == "" {
			return "", "", fmt.Errorf("scratch:// path %q but this run has no scratch root", p)
		}
		return job.ScratchRoot, rest, nil
	}
	if job.WorkspaceRoot == "" {
		return "", "", fmt.Errorf("no workspace sandbox configured")
	}
	return job.WorkspaceRoot, p, nil
}

func escapeErr(p string, err error) error {
	if errors.Is(err, os.ErrInvalid) ||
		strings.Contains(err.Error(), "escapes") ||
		strings.Contains(err.Error(), "outside") {
		return fmt.Errorf("path %q escapes its sandbox root", p)
	}
	return err
}

// ParseManifest maps the authored (camelCase) manifest JSON onto core.Manifest,
// applying the same defaults the engine expects (batch execution, long-lived
// process, "script" provider). Inputs/Outputs/RequiresConnections reuse the
// core types directly since their field names already match. The manifest JSON
// comes from the Node drop host (`drophost.mjs --emit-manifest`) at install
// time, or from a generate-time-embedded manifests.json for official drops.
func ParseManifest(raw []byte) (core.Manifest, error) {
	var ts struct {
		ID                  string                       `json:"id"`
		Version             string                       `json:"version"`
		Label               string                       `json:"label"`
		Summary             string                       `json:"summary"`
		Description         string                       `json:"description"`
		Integration         string                       `json:"integration"`
		Category            string                       `json:"category"`
		Icon                string                       `json:"icon"`
		BrandLogo           string                       `json:"brandLogo"`
		Color               string                       `json:"color"`
		Tags                []string                     `json:"tags"`
		Inputs              []core.Port                  `json:"inputs"`
		Outputs             []core.Port                  `json:"outputs"`
		ParamsSchema        json.RawMessage              `json:"paramsSchema"`
		RequiresConnections []core.ConnectionRequirement `json:"requiresConnections"`
		Egress              []string                     `json:"egress"`
		Idempotent          bool                         `json:"idempotent"`
		RetryPolicy         string                       `json:"retryPolicy"`
		ExecutionModel      string                       `json:"executionModel"`
		Examples            []struct {
			Title  string          `json:"title"`
			Params json.RawMessage `json:"params"`
			Notes  string          `json:"notes"`
		} `json:"examples"`
	}
	if err := json.Unmarshal(raw, &ts); err != nil {
		return core.Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	if ts.Summary == "" {
		return core.Manifest{}, fmt.Errorf("drop %q: manifest.summary is required", ts.ID)
	}

	em := core.ExecutionModel(ts.ExecutionModel)
	if em == "" {
		em = core.ExecutionBatch
	}
	rp := core.RetryPolicy(ts.RetryPolicy)
	if rp == "" {
		rp = core.RetryNever
	}
	cat := ts.Category
	if cat == "" {
		cat = "external"
	}
	examples := make([]core.ParamsExample, 0, len(ts.Examples))
	for _, e := range ts.Examples {
		params := e.Params
		if len(params) == 0 {
			params = json.RawMessage("{}")
		}
		examples = append(examples, core.ParamsExample{Title: e.Title, Params: params, Notes: e.Notes})
	}

	return core.Manifest{
		ID:                  ts.ID,
		Version:             ts.Version,
		Label:               ts.Label,
		Summary:             ts.Summary,
		Description:         ts.Description,
		Integration:         ts.Integration,
		Category:            cat,
		Icon:                ts.Icon,
		BrandLogo:           ts.BrandLogo,
		Color:               ts.Color,
		Tags:                ts.Tags,
		Inputs:              ts.Inputs,
		Outputs:             ts.Outputs,
		ParamsSchema:        ts.ParamsSchema,
		RequiresConnections: ts.RequiresConnections,
		Egress:              ts.Egress,
		Idempotent:          ts.Idempotent,
		RetryPolicy:         rp,
		ExecutionModel:      em,
		ProcessModel:        core.ProcessLongLived,
		Provider:            "script",
		Examples:            examples,
	}, nil
}
