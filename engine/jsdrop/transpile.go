// Package jsdrop is the scripted-drop catalog: it transpiles authored TS to the
// ESM module the out-of-process Node drop host runs, and tracks per-tenant,
// version-pinned drops. Execution happens in the Node host (engine/containerdrop),
// wired in via the catalog's Run hook — there is no in-process JS engine here.
package jsdrop

import (
	"fmt"
	"net/http"

	esbuild "github.com/evanw/esbuild/pkg/api"
)

// HTTPDoer is the guarded HTTP client a drop's fetch routes through (the broker
// mediates it host-side). Shared with engine/containerdrop.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// FileStore backs a drop's ctx.files, confined to the job's sandbox roots.
type FileStore interface {
	Read(path string) ([]byte, error)
	Write(path string, data []byte) error
	Exists(path string) (bool, error)
}

// TranspileESM strips TS types and emits a single ESM module, preserving the
// `export default` — the form the Node drop host imports. No bundling:
// author-side `hz-drops bundle` inlines npm/multi-file imports; this just
// handles plain TS→JS for single-file drops. Exported so the daemon's Node
// manifest extractor (engine/containerdrop) can produce the same module shape
// it asks Node to read.
func TranspileESM(src string) (string, error) {
	r := esbuild.Transform(src, esbuild.TransformOptions{
		Loader: esbuild.LoaderTS,
		Format: esbuild.FormatESModule,
		Target: esbuild.ES2020,
	})
	if len(r.Errors) > 0 {
		return "", fmt.Errorf("transpile (esm): %s", r.Errors[0].Text)
	}
	return string(r.Code), nil
}
