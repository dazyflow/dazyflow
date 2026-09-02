// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package net

import (
	"context"
	"fmt"
	stdnet "net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/dazyflow/dazyflow/core"
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

// selfOrigin is every origin that reaches this instance — the public one
// (DAZYFLOW_PUBLIC_BASE_URL) and the address the gateway listens on — in the
// canonical form originOf produces. Empty when unset.
//
// It exists so an outbound HTTP step can tell "I am calling my own daemon"
// from "I am calling a third party". A call to ourselves gets the
// trigger-chain depth header, which is what stops a flow whose HTTP step
// hits its own trigger URL from running forever. A call to anyone else must
// NOT carry it: it would leak our run topology to an unrelated service.
var selfOrigin atomic.Value // map[string]struct{} of canonical origins

// SetSelfOrigin records this instance's public base URL. Called once at
// startup; safe to leave unset (no self-directed request is then
// recognized, and the trigger endpoints still refuse a chain that arrives
// with a depth header).
func SetSelfOrigin(baseURL string) { SetSelfOrigins(baseURL) }

// SetSelfOrigins records every base URL that reaches this instance: the
// public one, and the address it listens on. One flow's author types the
// public name and another types the address the daemon answers on inside
// the container — both arrive here, so both have to be recognized as us or
// the one that isn't gets no depth header and its loop never breaks.
//
// Loopback spellings are expanded against each other (localhost, 127.0.0.1,
// [::1] on the same scheme and port), since they are the same machine by
// definition. A second PUBLIC name for this instance is not something a URL
// can be asked about — pass it here too.
func SetSelfOrigins(baseURLs ...string) {
	set := make(map[string]struct{}, len(baseURLs))
	for _, raw := range baseURLs {
		origin := canonicalOrigin(raw)
		if origin == "" {
			continue
		}
		set[origin] = struct{}{}
		for _, alias := range loopbackAliases(origin) {
			set[alias] = struct{}{}
		}
	}
	selfOrigin.Store(set)
}

// IsSelfDirected reports whether rawURL points back at this instance.
func IsSelfDirected(rawURL string) bool {
	set, _ := selfOrigin.Load().(map[string]struct{})
	if len(set) == 0 {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	return isSelfDirectedURL(u)
}

// isSelfDirectedURL is IsSelfDirected for a URL that is already parsed —
// the shape the transport has, and it runs on every outbound request.
func isSelfDirectedURL(u *url.URL) bool {
	set, _ := selfOrigin.Load().(map[string]struct{})
	if len(set) == 0 {
		return false
	}
	origin := originOf(u)
	if origin == "" {
		return false
	}
	_, ok := set[origin]
	return ok
}

// loopbackNames are the spellings of "this machine" that a flow author (or
// a compose file) might type. Any of them names the daemon that dials it.
var loopbackNames = []string{"localhost", "127.0.0.1", "[::1]"}

// loopbackAliases returns the other spellings of a canonical loopback
// origin, same scheme and port. Empty for a non-loopback host.
func loopbackAliases(origin string) []string {
	scheme, rest, ok := strings.Cut(origin, "://")
	if !ok {
		return nil
	}
	host, port := rest, ""
	if i := strings.LastIndex(rest, ":"); i > strings.LastIndex(rest, "]") {
		host, port = rest[:i], rest[i:]
	}
	if !isLoopbackHost(host) {
		return nil
	}
	out := make([]string, 0, len(loopbackNames))
	for _, name := range loopbackNames {
		if alias := scheme + "://" + name + port; alias != origin {
			out = append(out, alias)
		}
	}
	return out
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := stdnet.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// canonicalOrigin reduces a URL to a comparable scheme://host. Compared as
// raw strings, every spelling of one origin that a URL parser treats as
// equal was a way to reach our own trigger endpoints as "a third party" and
// so without the depth header: the default port written out (or left off)
// against a base URL that does the opposite, and the trailing root dot.
// Returns "" when there is no host to compare.
func canonicalOrigin(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return originOf(u)
}

// originOf is canonicalOrigin for an already-parsed URL.
func originOf(u *url.URL) string {
	if u == nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	// Hostname() drops the port and the brackets of an IPv6 literal; the
	// trailing dot is the DNS root, which resolves the same.
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return ""
	}
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // IPv6 literal, as it appears in a URL
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host
}

// triggerDepthTransport carries core.TriggerDepthHeader on every request
// that reaches THIS instance, and strips it from every request that does
// not.
//
// It sits in the shared client rather than in the step that posts. The stamp
// is what stops a flow from triggering itself, and a flow has several ways to
// post to a URL of its author's choosing: the Webhook drop, an upload or
// download with a method, a connection-configured sender, and the daemon's
// own failure webhook each build their own request. Written into the drop
// that posts, it was written into one of them — and the loop the HTTP step
// could no longer run came straight back through the drop whose whole purpose
// is POSTing to a URL. They all dial through buildClient.
//
// A RoundTripper also sees each redirect hop as its own request, so a 30x off
// our origin cannot carry the header away with it.
type triggerDepthTransport struct{ base http.RoundTripper }

func (t *triggerDepthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	self := isSelfDirectedURL(req.URL)
	if !self && req.Header.Get(core.TriggerDepthHeader) == "" {
		return t.base.RoundTrip(req) // nothing to add or remove
	}
	// A RoundTripper must not modify the request it is handed.
	r := req.Clone(req.Context())
	if self {
		// Set, not "fill in": on a call to ourselves the daemon's count is
		// the authority, so a step that types the header into its own
		// headers param cannot hold the chain at zero.
		r.Header.Set(core.TriggerDepthHeader, strconv.Itoa(core.TriggerDepth(req.Context())+1))
	} else {
		r.Header.Del(core.TriggerDepthHeader)
	}
	return t.base.RoundTrip(r)
}
