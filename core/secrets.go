package core

import "context"

type SecretProvider interface {
	Name() string
	Get(ctx context.Context, ref string) (string, error)
}
