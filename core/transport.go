package core

import "context"

type Transport interface {
	Manifest() Manifest
	Execute(ctx context.Context, job Job, progress chan<- Progress) (Result, error)
}
