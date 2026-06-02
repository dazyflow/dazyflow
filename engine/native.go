package engine

import (
	"context"

	"git.sr.ht/~klahr/hazyflow/core"
)

// NativeDrop is a module implemented as Go code, executed by direct function
// call in the same process as the engine. Modules construct one of these and
// hand it to Register during init().
type NativeDrop struct {
	Manifest core.Manifest
	Execute  func(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error)
}

// nativeTransport adapts a NativeDrop to the core.Transport interface. Kept
// unexported — callers should get one via the registry / resolver.
type nativeTransport struct {
	node NativeDrop
}

func (t *nativeTransport) Manifest() core.Manifest { return t.node.Manifest }

func (t *nativeTransport) Execute(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	return t.node.Execute(ctx, job, progress)
}
