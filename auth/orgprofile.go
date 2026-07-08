// SPDX-FileCopyrightText: 2026 Joachim Klahr
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

	// Platform-admin moderation, mirroring auth.User: Status is "active"
	// or "suspended". A suspended org keeps its data but its scheduled and
	// triggered flows stop firing (the scheduler and inbound webhook/form
	// paths skip suspended tenants) and its members are locked out at
	// auth. SuspendedAt / SuspendReason feed the audit trail and the
	// operator UI. A ban additionally blocklists the org's members'
	// re-signup; the profile row only tracks the suspension. omitempty
	// keeps existing stores byte-compatible.
	Status        string     `json:"status,omitempty"`
	SuspendedAt   *time.Time `json:"suspended_at,omitempty"`
	SuspendReason string     `json:"suspend_reason,omitempty"`
}

// Suspended reports whether a platform admin has locked this org. Empty
// status reads as active, so pre-moderation rows need no backfill.
func (p OrgProfile) Suspended() bool { return p.Status == StatusSuspended }

// OrgProfileStore is the lookup boundary. Tenants with no profile
// row return ErrUnknownOrgProfile; the UI falls back to the raw
// tenant ID in that case, so a missing profile is non-fatal.
type OrgProfileStore interface {
	GetOrgProfile(ctx context.Context, tenant string) (OrgProfile, error)
	PutOrgProfile(ctx context.Context, p OrgProfile) error
	ListOrgProfiles(ctx context.Context, tenants []string) (map[string]OrgProfile, error)
	// GetOrgProfileBySubdomain resolves a claimed subdomain label back to its
	// org profile (and thus tenant). Returns ErrUnknownOrgProfile when no org
	// has claimed that label. The lookup is case-insensitive on the label.
	GetOrgProfileBySubdomain(ctx context.Context, subdomain string) (OrgProfile, error)
}

var (
	ErrUnknownOrgProfile = errors.New("no profile for tenant")
	// ErrSubdomainTaken is returned by PutOrgProfile when the requested
	// subdomain is already claimed by a DIFFERENT org. The handler maps it to
	// a 409 so the UI can say "that subdomain is taken" rather than a 500.
	ErrSubdomainTaken = errors.New("subdomain already taken")
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

// IsServedInfraSubdomain reports whether label is a reserved infrastructure
// subdomain that Dazyflow serves and should be granted an on-demand TLS
// certificate. Input is normalized (lowercased/trimmed) to match host parsing.
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
