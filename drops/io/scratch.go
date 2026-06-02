package io

import (
	"git.sr.ht/~klahr/hazyflow/drops/internal/sandbox"
)

// Re-export the shared sandbox helpers under the same names the io
// package's drops have been calling since they lived here directly.
// Centralising the implementation in drops/internal/sandbox lets
// integrations outside io/ (gmail attachments, future drops) share the
// same scratch:// scheme + path-traversal defence without duplicating
// the logic. New callers should import internal/sandbox directly.
var (
	openSandboxRoot = sandbox.OpenRoot
	isSandboxEscape = sandbox.IsEscape
)
