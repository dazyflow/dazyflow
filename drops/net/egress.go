package net

import (
	"fmt"
	stdnet "net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
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
	cleaned := make([]string, 0, len(entries))
	for _, e := range entries {
		if s := strings.TrimSpace(e); s != "" {
			cleaned = append(cleaned, s)
		}
	}
	if len(cleaned) == 0 {
		egressMu.Lock()
		egressActive = nil
		egressMu.Unlock()
		return nil
	}
	p := &egressPolicy{exact: make(map[string]struct{})}
	for _, e := range cleaned {
		switch {
		case strings.Contains(e, "/"):
			_, ipnet, err := stdnet.ParseCIDR(e)
			if err != nil {
				return fmt.Errorf("egress allowlist: bad CIDR %q: %w", e, err)
			}
			p.nets = append(p.nets, ipnet)
		case strings.HasPrefix(e, "*."):
			suffix := strings.ToLower(e[1:]) // "*.slack.com" → ".slack.com"
			// Require at least two labels after the dot so a wildcard
			// can't be a whole TLD (*.com) — i.e. ".slack.com" is OK,
			// ".com" is not.
			if strings.Count(suffix, ".") < 2 {
				return fmt.Errorf("egress allowlist: wildcard %q too broad (need *.domain.tld)", e)
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
	egressMu.Lock()
	egressActive = p
	egressMu.Unlock()
	return nil
}

// allowPrivateEgress gates the per-request `allow_private_networks` flow
// param on the http_request / http_download / http_upload drops. That param
// disables the SSRF guard, so honoring it from an untrusted flow lets any
// tenant reach cloud metadata (169.254.169.254), localhost, and internal
// services. It is therefore ignored unless the operator opts in via
// SetAllowPrivateEgress (wired from HAZYFLOW_ALLOW_PRIVATE_EGRESS). Default
// off: the param has no effect and the SSRF guard always applies.
var allowPrivateEgress atomic.Bool

// SetAllowPrivateEgress sets the operator opt-in for the
// allow_private_networks param. Called once at startup.
func SetAllowPrivateEgress(v bool) { allowPrivateEgress.Store(v) }

// PrivateEgressAllowed reports whether drops may honor a request's
// allow_private_networks param. A drop must AND its own param with this.
func PrivateEgressAllowed() bool { return allowPrivateEgress.Load() }

// EgressAllowed is the exported form of egressAllowed, so other
// integration drops that make user-influenced outbound requests
// (e.g. notify/webhook_send) share the one operator egress policy.
func EgressAllowed(rawURL string) error { return egressAllowed(rawURL) }

// egressAllowed reports nil if rawURL's host is permitted by the active
// allowlist, or an error describing the block. No active list = allowed.
func egressAllowed(rawURL string) error {
	egressMu.RLock()
	p := egressActive
	egressMu.RUnlock()
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
