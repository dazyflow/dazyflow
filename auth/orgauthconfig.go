package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// OrgAuthConfig is the per-tenant sign-in policy. Today it carries
// Google Workspace SSO config; future providers (Microsoft Entra,
// Okta, SAML) extend the same record. ClientSecret is intentionally
// stored alongside the public ClientID rather than in EncryptedSecrets
// because the org-auth store is admin-only and the secret lifecycle
// matches the org's lifecycle. Production deployments running
// Postgres get the Pg variant which can layer at-rest encryption.
//
// WorkspaceDomain restricts which Google accounts can sign into this
// org: the hd= claim on Google's response must match. Empty means
// any Google account whose email matches a member of the org may
// sign in (less strict, useful for personal-Gmail-using small teams).
type OrgAuthConfig struct {
	Tenant             string    `json:"tenant"`
	GoogleClientID     string    `json:"google_client_id"`
	GoogleClientSecret string    `json:"google_client_secret"`
	GoogleWorkspaceDomain string `json:"google_workspace_domain,omitempty"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// GoogleEnabled reports whether enough Google fields are populated
// for the sign-in handler to attempt an OAuth round-trip. Both
// client_id and secret are required; the domain is optional and
// enforced only when set.
func (c OrgAuthConfig) GoogleEnabled() bool {
	return strings.TrimSpace(c.GoogleClientID) != "" &&
		strings.TrimSpace(c.GoogleClientSecret) != ""
}

// OrgAuthStore is the lookup boundary. Operations are keyed on
// tenant ID; a tenant with no config returns ErrUnknownOrgAuth and
// the sign-in flow falls back to password-only.
type OrgAuthStore interface {
	GetOrgAuth(ctx context.Context, tenant string) (OrgAuthConfig, error)
	PutOrgAuth(ctx context.Context, cfg OrgAuthConfig) error
	DeleteOrgAuth(ctx context.Context, tenant string) error
}

var ErrUnknownOrgAuth = errors.New("no auth config for tenant")

// JSONOrgAuthStore is the JSON-file backing for dev / single-node.
type JSONOrgAuthStore struct {
	mu    sync.RWMutex
	path  string
	items map[string]OrgAuthConfig
}

func OpenJSONOrgAuthStore(path string) (*JSONOrgAuthStore, error) {
	s := &JSONOrgAuthStore{path: path, items: make(map[string]OrgAuthConfig)}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	var slice []OrgAuthConfig
	if err := json.Unmarshal(data, &slice); err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}
	for _, c := range slice {
		s.items[c.Tenant] = c
	}
	return s, nil
}

func (s *JSONOrgAuthStore) GetOrgAuth(_ context.Context, tenant string) (OrgAuthConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.items[tenant]
	if !ok {
		return OrgAuthConfig{}, ErrUnknownOrgAuth
	}
	return c, nil
}

func (s *JSONOrgAuthStore) PutOrgAuth(_ context.Context, cfg OrgAuthConfig) error {
	if cfg.Tenant == "" {
		return fmt.Errorf("tenant required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[cfg.Tenant] = cfg
	return s.flushLocked()
}

func (s *JSONOrgAuthStore) DeleteOrgAuth(_ context.Context, tenant string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, tenant)
	return s.flushLocked()
}

func (s *JSONOrgAuthStore) flushLocked() error {
	if s.path == "" {
		return nil
	}
	slice := make([]OrgAuthConfig, 0, len(s.items))
	for _, c := range s.items {
		slice = append(slice, c)
	}
	data, err := json.MarshalIndent(slice, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
