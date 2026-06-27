// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"context"
	"fmt"
	stdnet "net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Egress allowlist for http_request. The SSRF guard (ssrfGuard) blocks
// private/loopback/link-local IPs at dial time, but a tenant can still
// reach ANY public host. In a hosted/multi-tenant deployment that's an
// exfiltration and pivot risk, so an operator can pin egress to a set
// of permitted hosts.
//
// Policy is set once at startup via SetEgressAllowlist (the daemon wires
// it from --http-egress-allow), mirroring the SetTokenLookup /
// SetSecretWriter hook pattern so this package stays free of a daemon
// import. When unset (nil) every public host is allowed — the
// backward-compatible default; the flag is opt-in.
//
// Entries are one of:
//   - exact hostname     "api.stripe.com"
//   - wildcard subdomain "*.slack.com"  (matches a.slack.com, a.b.slack.com;
//                                         NOT the bare apex slack.com)
//   - IP / CIDR          "203.0.113.7", "203.0.113.0/24"
//
// Hostname checks happen before DNS on the URL's literal host; the IP
// SSRF guard still runs at dial, so a hostname that resolves to a
// private IP is blocked regardless of the allowlist.

type egressPolicy struct {
	exact     map[string]struct{} // lowercased exact hostnames
	suffixes  []string            // lowercased ".slack.com" for *.slack.com
	nets      []*stdnet.IPNet     // CIDR rules
	singleIPs []stdnet.IP         // bare-IP rules
}

var (
	egressMu     sync.RWMutex
	egressActive *egressPolicy // nil = allow all public hosts
)

// SetEgressAllowlist installs (or clears) the egress allowlist. Passing
// nil or an all-empty slice clears it (allow all public hosts). Returns
// an error if any entry is malformed so misconfiguration fails loudly at
// startup rather than silently allowing/denying.
func SetEgressAllowlist(entries []string) error {
	p, err := compileEgress(entries)
	if err != nil {
		return err
	}
	egressMu.Lock()
	egressActive = p // nil when entries were all-empty → allow all public hosts
	egressMu.Unlock()
	return nil
}

// compileEgress parses allowlist entries into a matcher. Returns (nil, nil)
// when the cleaned list is empty (meaning "no restriction"), or an error on a
// malformed entry. Shared by the global SetEgressAllowlist and the per-tenant
// EgressPolicy path so both compile and match identically.
func compileEgress(entries []string) (*egressPolicy, error) {
	cleaned := make([]string, 0, len(entries))
	for _, e := range entries {
		if s := strings.TrimSpace(e); s != "" {
			cleaned = append(cleaned, s)
		}
	}
	if len(cleaned) == 0 {
		return nil, nil
	}
	p := &egressPolicy{exact: make(map[string]struct{})}
	for _, e := range cleaned {
		switch {
		case strings.Contains(e, "/"):
			_, ipnet, err := stdnet.ParseCIDR(e)
			if err != nil {
				return nil, fmt.Errorf("egress allowlist: bad CIDR %q: %w", e, err)
			}
			p.nets = append(p.nets, ipnet)
		case strings.HasPrefix(e, "*."):
			suffix := strings.ToLower(e[1:]) // "*.slack.com" → ".slack.com"
			// Require at least two labels after the dot so a wildcard
			// can't be a whole TLD (*.com) — i.e. ".slack.com" is OK,
			// ".com" is not.
			if strings.Count(suffix, ".") < 2 {
				return nil, fmt.Errorf("egress allowlist: wildcard %q too broad (need *.domain.tld)", e)
			}
			p.suffixes = append(p.suffixes, suffix)
		default:
			if ip := stdnet.ParseIP(e); ip != nil {
				p.singleIPs = append(p.singleIPs, ip)
			} else {
				p.exact[strings.ToLower(e)] = struct{}{}
			}
		}
	}
	return p, nil
}

// allowPrivateEgress gates the per-request `allow_private_networks` flow
// param on the http_request / http_download / http_upload drops. That param
// disables the SSRF guard, so honoring it from an untrusted flow lets any
// tenant reach cloud metadata (169.254.169.254), localhost, and internal
// services. It is therefore ignored unless the operator opts in via
// SetAllowPrivateEgress (wired from DAZYFLOW_ALLOW_PRIVATE_EGRESS). Default
// off: the param has no effect and the SSRF guard always applies.
var allowPrivateEgress atomic.Bool

// SetAllowPrivateEgress sets the operator opt-in for the
// allow_private_networks param. Called once at startup.
func SetAllowPrivateEgress(v bool) { allowPrivateEgress.Store(v) }

// PrivateEgressAllowed reports whether drops may honor a request's
// allow_private_networks param. A drop must AND its own param with this.
func PrivateEgressAllowed() bool { return allowPrivateEgress.Load() }

// EgressAllowed reports nil if rawURL's host is permitted by the active
// operator allowlist, or an error describing the block. No active list =
// allowed. This is the single, exported entry point for the egress/SSRF
// policy: every caller — in this package and in the integration drops
// (gmail, stripe, sheets, git, notify/webhook_send, …) — funnels through it
// so the policy is single-sourced.
func EgressAllowed(rawURL string) error {
	egressMu.RLock()
	p := egressActive
	egressMu.RUnlock()
	return p.allow(rawURL)
}

// allow reports nil if rawURL's host is permitted by this compiled policy. A
// nil policy means "no restriction" → allowed.
func (p *egressPolicy) allow(rawURL string) error {
	if p == nil {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("egress_blocked: cannot parse URL")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("egress_blocked: URL has no host")
	}
	if ip := stdnet.ParseIP(host); ip != nil {
		for _, single := range p.singleIPs {
			if single.Equal(ip) {
				return nil
			}
		}
		for _, n := range p.nets {
			if n.Contains(ip) {
				return nil
			}
		}
		return fmt.Errorf("egress_blocked: %s not in egress allowlist", host)
	}
	if _, ok := p.exact[host]; ok {
		return nil
	}
	for _, suffix := range p.suffixes {
		if strings.HasSuffix(host, suffix) {
			return nil
		}
	}
	return fmt.Errorf("egress_blocked: host %q not in egress allowlist", host)
}

// EgressPolicy resolves the per-tenant egress allowlist. A multi-tenant
// deployment implements it (e.g. backed by per-tenant config) and registers it
// with SetEgressPolicy — wired by the daemon just like SetEgressAllowlist wires
// the operator-global list. AllowlistFor returns a tenant's allowed entries and
// whether a per-tenant policy exists; when it returns (_, false) or an empty
// list, egress falls back to the global list. This lets one operator host
// many tenants with independent egress isolation instead of a single shared
// allowlist.
//
// (Defined here, alongside the enforcement, rather than in core: core is the
// lower layer and must not depend on the net package's matcher. The daemon
// registers a concrete policy the same way it registers the global allowlist.)
type EgressPolicy interface {
	AllowlistFor(tenant string) (entries []string, ok bool)
}

var (
	egressPolicyMu sync.RWMutex
	egressPolicy_  EgressPolicy
)

// SetEgressPolicy installs (or clears, with nil) the per-tenant egress policy
// resolver. Off by default — egress uses only the global allowlist until a
// policy is registered.
func SetEgressPolicy(p EgressPolicy) {
	egressPolicyMu.Lock()
	egressPolicy_ = p
	egressPolicyMu.Unlock()
}

// EgressAllowedFor is the tenant-aware egress check. It resolves the tenant
// from ctx and, when a per-tenant policy supplies a non-empty allowlist for
// that tenant, enforces it; otherwise it falls back to the operator-global
// allowlist (EgressAllowed). A per-tenant policy that fails to compile fails
// closed (blocked) rather than silently allowing. Callers that have a request
// context should prefer this over EgressAllowed so multi-tenant isolation
// applies.
func EgressAllowedFor(ctx context.Context, rawURL string) error {
	egressPolicyMu.RLock()
	resolver := egressPolicy_
	egressPolicyMu.RUnlock()
	if resolver != nil {
		tenant, _ := core.TenantFromContext(ctx)
		if entries, ok := resolver.AllowlistFor(tenant); ok && len(entries) > 0 {
			p, err := compileEgress(entries)
			if err != nil {
				return fmt.Errorf("egress_blocked: tenant egress policy is invalid: %v", err)
			}
			return p.allow(rawURL)
		}
	}
	return EgressAllowed(rawURL)
}
