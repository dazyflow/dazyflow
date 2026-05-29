# Artifact store — design sketch

> **Shelved (2026-05) — design kept, implementation backed out.** A spike
> implemented `core.ArtifactStore` + `ArtifactKey` + `LocalArtifactStore` and
> migrated `file_write`, but it was reverted on two findings:
> 1. **It can't replace the sandbox.** ~20 drops touch local files, and
>    `sqlite_*`, `shell`, and `git_*` genuinely need a real local filesystem
>    (you can't run SQLite or git on an object store). So the store is only for
>    *blob* drops (file read/write, http download, excel) — it would be a
>    *second, partial* backend coexisting with the sandbox, i.e. more complexity.
> 2. **It's premature.** Containerized drops will reshape file handling anyway:
>    the store's natural home is the **container broker's `files` capability**
>    (see `engine/containerdrop/DESIGN.md`), where it's the single model for a
>    drop's persistent/shared blobs while the container's own FS covers local
>    working files. Build it *there*, when the broker is built — not as an
>    in-process partial migration now.
>
> The interface + `ArtifactKey` confinement below are the reference for that
> future work. Scaling isn't needed yet, and there's no back-compat constraint,
> so the simplest codebase keeps one file model (the sandbox) until then.

Status: **design sketch, no code yet.** The highest-leverage scaling change:
move drop file I/O off local disk onto a pluggable backend (local FS for dev,
object storage for prod), so the control plane can run as horizontal replicas
and drops can execute on a separate plane (containers) without co-location.

## The problem

Today a job gets two **local filesystem paths** — `WorkspaceRoot` (persistent,
per tenant+workspace) and `ScratchRoot` (ephemeral, per run) — and file drops
read/write within them via `sandbox.OpenRoot` (`os.Root` confinement). Paths
carry a scheme: bare = workspace, `scratch://` = scratch.

That breaks the moment there's more than one node:
- **Horizontal replicas / a separate execution plane** — the worker (or drop
  container) that runs a node may not be the machine that holds the local
  directory. Local disk isn't shared.
- **Cross-drop refs require co-location** — drop A writes `scratch://out.csv`,
  drop B reads it. Today that only works because they ran on the same disk.

File `Ref`s thread through every file drop, the jsdrop/broker `files` capability,
and the engine — so this is the least-reversible assumption in the system. Fix
it before customers, behind an interface, with local FS preserved for dev.

## The seam: a key-based store + a confinement resolver

Split the concern in two:

1. **`core.ArtifactStore`** — a dumb, key-based, streaming blob backend. It knows
   nothing about tenancy or traversal; it just reads/writes keys.
2. **`core.ArtifactKey(job, userPath)`** — the confinement resolver. It maps a
   job + a user-facing path to a tenant/scope-confined key. This is where
   traversal safety lives — the object-storage analog of `os.Root`.

Confinement moves from "open an OS root" to "build a key that can't escape its
prefix." Same guarantee, backend-independent.

```go
// core/artifact.go (sketch)

// ArtifactStore is the blob backend for drop file I/O. Keys are opaque, "/"-
// delimited strings produced by ArtifactKey — the store never reasons about
// tenancy or path traversal. Streaming (io.Reader/Writer), not []byte, so large
// files don't have to fit in memory (important for object storage).
type ArtifactStore interface {
    Open(ctx context.Context, key string) (io.ReadCloser, error)
    Create(ctx context.Context, key string) (io.WriteCloser, error) // committed on Close
    Stat(ctx context.Context, key string) (ArtifactInfo, error)
    Delete(ctx context.Context, key string) error
    List(ctx context.Context, prefix string) ([]string, error)
    // DeletePrefix reclaims a whole scope (scratch teardown).
    DeletePrefix(ctx context.Context, prefix string) error
}

type ArtifactInfo struct {
    Exists bool
    Size   int64
}
```

### The confinement resolver (security-critical)

```go
// ArtifactKey maps a job + user path to a confined storage key, preserving
// today's scheme: "scratch://" → the run's ephemeral scope; bare/"workspace://"
// → the persistent per-tenant+workspace scope. Generalizes sandbox.Resolve
// from "pick a local root" to "build a key".
func ArtifactKey(job Job, userPath string) (string, error) {
    scope, p := "workspace", userPath
    if rest, ok := strings.CutPrefix(userPath, "scratch://"); ok {
        scope, p = "scratch", rest
    } else if rest, ok := strings.CutPrefix(userPath, "workspace://"); ok {
        p = rest
    }
    // Traversal safety: prefix with "/" then Clean — any ".." collapses and
    // cannot rise above root. This is the object-store equivalent of os.Root.
    clean := strings.TrimPrefix(path.Clean("/"+p), "/")
    if clean == "" {
        return "", fmt.Errorf("empty artifact path")
    }
    switch scope {
    case "scratch":
        if job.Run == "" {
            return "", fmt.Errorf("scratch path %q but run has no id", userPath)
        }
        return "scratch/" + job.Run + "/" + clean, nil
    default:
        if job.Tenant == "" || job.Workspace == "" {
            return "", fmt.Errorf("no workspace scope for %q", userPath)
        }
        return "workspace/" + job.Tenant + "/" + job.Workspace + "/" + clean, nil
    }
}
```

Note: this requires `Job` to carry **logical scope identifiers** — `Tenant`
(have it), `Workspace` (logical name, add — today it's encoded in the
`WorkspaceRoot` *path*), and `Run` (add, or reuse the run id). `WorkspaceRoot`/
`ScratchRoot` (raw local paths) get deprecated in favor of these + the store.

## Backends

```go
// LocalArtifactStore — dev + back-compat. Keys map under a base dir with
// os.Root confinement as defense-in-depth. Preserves today's behavior:
// pointing base at the existing sandbox root reproduces WorkspaceRoot/ScratchRoot.
type LocalArtifactStore struct{ Base string }

// ObjectArtifactStore — prod. Keys are object names in a bucket (S3/GCS/MinIO);
// Open/Create stream via the SDK. Cross-replica safe: same key → same object,
// regardless of which node resolves it.
type ObjectArtifactStore struct{ Bucket string; Client S3API }
```

Same interface; chosen by config. **This is what makes any replica or any drop
container resolve the same `Ref` to the same bytes** — the scaling win.

## Quota

Today `FSQuota` computes usage by **walking the local directory** — impossible
(or expensive) on object storage. The `core.QuotaReserver` interface stays
(reserve-and-hold is unchanged); only the "used" computation changes:
- track usage in a **per-tenant counter** (incremented on `Create` close,
  decremented on `Delete`), or
- read it from the object store's inventory / a `List`+`Stat` sum cached with a TTL.

So a write becomes: `ArtifactKey` → reserve(tenant, size) → `store.Create` →
copy → commit; release on completion. The store reports `Stat.Size` for the
accounting.

## How it threads (and how the container broker uses it)

- The **engine** wires one `ArtifactStore` (local or object) and stamps the job's
  logical scope (`Tenant`/`Workspace`/`Run`) instead of raw root paths.
- **File drops** (`file_read`, `file_write`, `excel_read`, `http_download`) and
  **jsdrop's `files`** switch from `sandbox.OpenRoot` to
  `ArtifactKey` + `store.Open/Create`.
- The **container broker's `files` capability** (from the containerd design) is
  literally this store, scoped by the job — the drop never touches storage
  directly; the broker resolves the key and streams.

So the same abstraction serves in-process drops, the broker, and the engine.

## Why this is the scaling unlock

- **Stateless control plane** — no local disk to pin a job to a node; any
  replica can handle any job's files.
- **Separate execution plane** — a drop container on any host reads/writes the
  same blobs via the broker.
- **Cross-drop refs just work** — `scratch://out.csv` is a key; whoever runs the
  next drop resolves the same key.
- **Scratch teardown** — `DeletePrefix("scratch/<run>/")`, or an object-store
  lifecycle rule on the `scratch/` prefix.

## Migration order (incremental, back-compat throughout)

1. Add `core.ArtifactStore` + `ArtifactKey` + `Job.Workspace`/`Job.Run`.
2. Implement `LocalArtifactStore` so that, pointed at the current sandbox base,
   it reproduces today's behavior. Engine provides it per job.
3. Migrate file drops + jsdrop `files` to the store (keep `WorkspaceRoot`/
   `ScratchRoot` populated during transition so nothing breaks mid-migration).
4. Add `ObjectArtifactStore`; switch by config. Now multi-replica works.
5. Move quota off dir-walk to counter/inventory accounting.
6. Drop the raw-path fields once all callers use the store.

## Open questions

- **`Job.Workspace` identity** — logical name vs id; how it maps from today's
  `WorkspaceRoot` path.
- **Large-file streaming limits** + multipart upload thresholds for the object
  backend.
- **Consistency** — object stores are read-after-write for new keys (S3 is now
  strongly consistent); fine, but worth pinning the assumption.
- **Caching** a hot scratch blob near the execution plane (later optimization).
