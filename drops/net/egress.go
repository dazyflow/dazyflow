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

// The SSRF guard blocks where a request may go by ADDRESS; this bounds it by
// NAME, so an operator can say which third parties a tenant may reach at all.

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

// Passing nil clears it, which allows everything.
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
			// At least two labels after the dot, or "*.com" would allow the internet.
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

// A per-node flag that disables the SSRF guard, so it is refused unless the
// operator opted the whole deployment in.
var allowPrivateEgress atomic.Bool

func SetAllowPrivateEgress(v bool) { allowPrivateEgress.Store(v) }

func PrivateEgressAllowed() bool { return allowPrivateEgress.Load() }

func EgressAllowed(rawURL string) error {
	egressMu.RLock()
	p := egressActive
	egressMu.RUnlock()
	return p.allow(rawURL)
}

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

type EgressPolicy interface {
	AllowlistFor(tenant string) (entries []string, ok bool)
}

var (
	egressPolicyMu sync.RWMutex
	egressPolicy_  EgressPolicy
)

func SetEgressPolicy(p EgressPolicy) {
	egressPolicyMu.Lock()
	egressPolicy_ = p
	egressPolicyMu.Unlock()
}

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

var selfOrigin atomic.Value // map[string]struct{} of canonical origins

func SetSelfOrigin(baseURL string) { SetSelfOrigins(baseURL) }

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

var loopbackNames = []string{"localhost", "127.0.0.1", "[::1]"}

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

func canonicalOrigin(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return originOf(u)
}

func originOf(u *url.URL) string {
	if u == nil || u.Host == "" {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
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

type triggerDepthTransport struct{ base http.RoundTripper }

func (t *triggerDepthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	self := isSelfDirectedURL(req.URL)
	if !self && req.Header.Get(core.TriggerDepthHeader) == "" {
		return t.base.RoundTrip(req) // nothing to add or remove
	}
	r := req.Clone(req.Context())
	if self {
		r.Header.Set(core.TriggerDepthHeader, strconv.Itoa(core.TriggerDepth(req.Context())+1))
	} else {
		r.Header.Del(core.TriggerDepthHeader)
	}
	return t.base.RoundTrip(r)
}
