// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
)

type OrgProfile struct {
	Tenant      string `json:"tenant"`
	DisplayName string `json:"display_name"`
	// Icon is an optional org logo: a data: URL (uploaded SVG/PNG) or a
	// logical icon name. The UI renders an image when it's a data:/URL/
	// path and a glyph otherwise. Stored inline — kept small client-side.
	Icon string `json:"icon,omitempty"`
	// Subdomain is the org's chosen DNS label on a wildcard-domain deploy
	// ("klahr" → klahr.dazyflow.app). Unique across orgs (case-insensitive),
	// a valid DNS label, and never a reserved infrastructure name. Empty when
	// the org hasn't claimed one (or the deploy has no wildcard domain). It's
	// the user-facing alias the sign-in page resolves back to the immutable
	// tenant ID — see ValidateSubdomain + OrgProfileStore.GetOrgProfileBySubdomain.
	Subdomain string    `json:"subdomain,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`

	Status        string     `json:"status,omitempty"`
	SuspendedAt   *time.Time `json:"suspended_at,omitempty"`
	SuspendReason string     `json:"suspend_reason,omitempty"`
}

func (p OrgProfile) Suspended() bool { return p.Status == StatusSuspended }

type OrgProfileStore interface {
	GetOrgProfile(ctx context.Context, tenant string) (OrgProfile, error)
	PutOrgProfile(ctx context.Context, p OrgProfile) error
	ListOrgProfiles(ctx context.Context, tenants []string) (map[string]OrgProfile, error)
	GetOrgProfileBySubdomain(ctx context.Context, subdomain string) (OrgProfile, error)
}

var (
	ErrUnknownOrgProfile = errors.New("no profile for tenant")
	ErrSubdomainTaken    = errors.New("subdomain already taken")
	// ErrInvalidSubdomain is returned by ValidateSubdomain for a label that
	// isn't a usable DNS label or is reserved.
	ErrInvalidSubdomain = errors.New("invalid subdomain")
)

// subdomainLabel is a conservative DNS label: 1–63 chars, lowercase
// alphanumerics and internal hyphens, no leading/trailing hyphen. Mirrors the
// web's orgFromHost LABEL so what the UI accepts and what a host can carry
// agree exactly.
var subdomainLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// reservedSubdomains are infrastructure/marketing hosts a wildcard record
// captures; they must never map to an org. Kept in lockstep with the web's
// orgFromHost RESERVED set.
var reservedSubdomains = map[string]bool{
	"www": true, "app": true, "api": true, "admin": true, "auth": true,
	"static": true, "assets": true, "cdn": true, "mail": true, "smtp": true,
	"ftp": true, "ns": true, "ns1": true, "ns2": true, "blog": true,
	"docs": true, "status": true, "help": true, "support": true,
	"registry": true,
}

// servedInfraSubdomains are the reserved infrastructure hosts Dazyflow actually
// SERVES with their own site block + certificate (as opposed to reserved names
// we merely block from org claims). The on-demand TLS gate (tlsAllow) must
// authorize certs for these even though they're reserved and map to no org —
// otherwise Caddy has no way to obtain a cert for them (a name covered by the
// on-demand wildcard is excluded from proactive issuance). Keep this a strict
// subset of reservedSubdomains: only names we truly front.
var servedInfraSubdomains = map[string]bool{
	"docs": true,
}

func IsServedInfraSubdomain(label string) bool {
	return servedInfraSubdomains[strings.ToLower(strings.TrimSpace(label))]
}

// ValidateSubdomain normalizes and validates a requested subdomain label.
// Empty input is valid and normalizes to "" (clearing the subdomain). A
// non-empty label is lowercased and trimmed, then must be a valid DNS label
// (subdomainLabel) and not reserved (reservedSubdomains); otherwise it returns
// ErrInvalidSubdomain. Returns the normalized value to store.
func ValidateSubdomain(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", nil
	}
	if !subdomainLabel.MatchString(s) || reservedSubdomains[s] {
		return "", ErrInvalidSubdomain
	}
	return s, nil
}

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
