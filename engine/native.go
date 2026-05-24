package engine

import (
	"context"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// NativeNode is a module implemented as Go code, executed by direct function
// call in the same process as the engine. Modules construct one of these and
// hand it to Register during init().
type NativeNode struct {
	Manifest core.Manifest
	Execute  func(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error)
}

// nativeTransport adapts a NativeNode to the core.Transport interface. Kept
// unexported — callers should get one via the registry / resolver.
type nativeTransport struct {
	node NativeNode
}

func (t *nativeTransport) Manifest() core.Manifest { return t.node.Manifest }

func (t *nativeTransport) Execute(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	return t.node.Execute(ctx, job, progress)
}
