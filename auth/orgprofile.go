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
	"unicode"
)

// OrgProfile is the human-facing identity for an org — kept separate
// from the immutable tenant ID (the random usr_<hex> minted at signup)
// because users want to rename their org without changing every
// downstream reference. The ID lives in URLs, audit logs, webhook
// paths; the DisplayName is what shows up in the switcher and admin
// header.
//
// Currently we only carry the display name; future expansion (logo,
// billing email, support contact, custom domain) lives here.
type OrgProfile struct {
	Tenant      string    `json:"tenant"`
	DisplayName string    `json:"display_name"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// OrgProfileStore is the lookup boundary. Tenants with no profile
// row return ErrUnknownOrgProfile; the UI falls back to the raw
// tenant ID in that case, so a missing profile is non-fatal.
type OrgProfileStore interface {
	GetOrgProfile(ctx context.Context, tenant string) (OrgProfile, error)
	PutOrgProfile(ctx context.Context, p OrgProfile) error
	ListOrgProfiles(ctx context.Context, tenants []string) (map[string]OrgProfile, error)
}

var ErrUnknownOrgProfile = errors.New("no profile for tenant")

// JSONOrgProfileStore is the dev / single-node backing. Same shape as
// the rest of the JSON auth stores in this package.
type JSONOrgProfileStore struct {
	mu    sync.RWMutex
	path  string
	items map[string]OrgProfile
}

func OpenJSONOrgProfileStore(path string) (*JSONOrgProfileStore, error) {
	s := &JSONOrgProfileStore{path: path, items: make(map[string]OrgProfile)}
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
	var slice []OrgProfile
	if err := json.Unmarshal(data, &slice); err != nil {
		return nil, fmt.Errorf("parse %q: %w", path, err)
	}
	for _, p := range slice {
		s.items[p.Tenant] = p
	}
	return s, nil
}

func (s *JSONOrgProfileStore) GetOrgProfile(_ context.Context, tenant string) (OrgProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.items[tenant]
	if !ok {
		return OrgProfile{}, ErrUnknownOrgProfile
	}
	return p, nil
}

func (s *JSONOrgProfileStore) PutOrgProfile(_ context.Context, p OrgProfile) error {
	if p.Tenant == "" {
		return fmt.Errorf("tenant required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[p.Tenant] = p
	return s.flushLocked()
}

// ListOrgProfiles is a bulk lookup the gateway uses to populate the
// org switcher: one call returns the display names for every org the
// user belongs to. Unknown tenants are silently absent from the
// result map (the UI falls back to the raw ID).
func (s *JSONOrgProfileStore) ListOrgProfiles(_ context.Context, tenants []string) (map[string]OrgProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]OrgProfile, len(tenants))
	for _, t := range tenants {
		if p, ok := s.items[t]; ok {
			out[t] = p
		}
	}
	return out, nil
}

func (s *JSONOrgProfileStore) flushLocked() error {
	if s.path == "" {
		return nil
	}
	slice := make([]OrgProfile, 0, len(s.items))
	for _, p := range s.items {
		slice = append(slice, p)
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

// DefaultOrgDisplayName picks a sensible initial name for the org an
// account is signing up into. Logic:
//
//  1. Take the email's domain ("alice@acme.test" → "acme.test").
//  2. Trim generic prefixes ("my.acme.com" → "acme.com") that often
//     wrap a real brand.
//  3. Take the leftmost remaining label ("acme.test" → "acme").
//  4. Title-case it ("acme" → "Acme").
//
// For widely-shared consumer email providers (gmail.com, outlook.com,
// hotmail.com, yahoo.com, icloud.com) the brand name is itself the
// least useful default — those users are sole proprietors who want
// their *own* name, not "Gmail". We fall back to the local-part
// title-cased in that case ("alice@gmail.com" → "Alice").
func DefaultOrgDisplayName(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 1 || at == len(email)-1 {
		return ""
	}
	local := strings.TrimSpace(email[:at])
	domain := strings.ToLower(strings.TrimSpace(email[at+1:]))
	if isConsumerEmailDomain(domain) {
		return titleize(local)
	}
	// Strip leading generic labels people prepend ("my.", "team.",
	// "mail.") so the brand is what surfaces.
	parts := strings.Split(domain, ".")
	for len(parts) > 1 && isGenericDomainPrefix(parts[0]) {
		parts = parts[1:]
	}
	if len(parts) == 0 || parts[0] == "" {
		return titleize(local)
	}
	return titleize(parts[0])
}

func isConsumerEmailDomain(domain string) bool {
	switch domain {
	case "gmail.com", "googlemail.com",
		"outlook.com", "hotmail.com", "live.com", "msn.com",
		"yahoo.com", "yahoo.co.uk", "ymail.com",
		"icloud.com", "me.com", "mac.com",
		"proton.me", "protonmail.com",
		"aol.com", "fastmail.com":
		return true
	}
	return false
}

func isGenericDomainPrefix(label string) bool {
	switch strings.ToLower(label) {
	case "my", "team", "mail", "email", "www", "app", "go":
		return true
	}
	return false
}

// titleize uppercases the first letter and lowercases the rest;
// strings.Title is deprecated and cases.Title pulls in golang.org/x.
// We only need ASCII-clean behaviour for an org default.
func titleize(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	for i := 1; i < len(r); i++ {
		r[i] = unicode.ToLower(r[i])
	}
	return string(r)
}
