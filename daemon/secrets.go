package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// EnvProvider resolves env://NAME by reading os.Getenv("NAME"). Missing
// vars are an error (rather than silently returning ""), so a typo or
// forgotten env var fails the job loudly instead of leaving the
// downstream module with an empty string where it expected an API key.
type EnvProvider struct{}

func (EnvProvider) Scheme() string { return "env" }

func (EnvProvider) Get(_ context.Context, name string) (string, error) {
	v, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("env var %q is not set", name)
	}
	if v == "" {
		return "", fmt.Errorf("env var %q is empty", name)
	}
	return v, nil
}

// BuiltinProvider holds an in-memory map of name → value. Production
// deployments load it from an encrypted file at startup; the contents
// never leave the daemon's memory (modules see resolved values, not
// references, in their Job).
type BuiltinProvider struct {
	mu      sync.RWMutex
	secrets map[string]string
}

func NewBuiltinProvider() *BuiltinProvider {
	return &BuiltinProvider{secrets: make(map[string]string)}
}

// NewBuiltinProviderFromFile reads a JSON map of name → value. The file
// format is the simplest thing that works:
//
//	{"stripe.api-key": "sk_live_...", "smtp.password": "..."}
//
// For production use, the file should be encrypted at rest (sealed
// secrets, age, vault-cli output, etc.); this loader doesn't do
// decryption itself.
func NewBuiltinProviderFromFile(path string) (*BuiltinProvider, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("builtin secrets file %q: %w", path, err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}
	p := NewBuiltinProvider()
	for k, v := range m {
		p.Set(k, v)
	}
	return p, nil
}

func (b *BuiltinProvider) Scheme() string { return "builtin" }

func (b *BuiltinProvider) Get(_ context.Context, name string) (string, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	v, ok := b.secrets[name]
	if !ok {
		return "", fmt.Errorf("builtin secret %q not found", name)
	}
	return v, nil
}

// Set lets tests (and admin paths) populate the provider without going
// through the file loader.
func (b *BuiltinProvider) Set(name, value string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.secrets[name] = value
}

// Compile-time interface checks.
var (
	_ core.SecretProvider = EnvProvider{}
	_ core.SecretProvider = (*BuiltinProvider)(nil)
)
