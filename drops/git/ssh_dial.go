// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package git

import (
	"context"
	"net"
	"net/url"
	"time"

	gogittransport "github.com/go-git/go-git/v5/plumbing/transport"
	"golang.org/x/net/proxy"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// go-git's ssh transport dials with its own net.Dialer and has no dialer hook,
// so the SSRF guard the https transport gets (InstallGuardedHTTPTransport) never
// ran for ssh: the only check was guardRepoURL's one-time DNS lookup, which a
// rebinding resolver answers differently at dial time. What go-git does offer is
// a proxy: ProxyOptions.URL is resolved through golang.org/x/net/proxy, whose
// scheme registry lets a "proxy" be any dialer. This one is a direct dial with
// the same after-DNS Control hook the http client uses.
const guardedDialScheme = "dazyflow-guarded-dial"

func init() {
	proxy.RegisterDialerType(guardedDialScheme, func(*url.URL, proxy.Dialer) (proxy.Dialer, error) {
		return guardedDialer{}, nil
	})
}

type guardedDialer struct{}

func (d guardedDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

func (guardedDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	nd := &net.Dialer{Timeout: 30 * time.Second, Control: hfnet.SSRFDialControl()}
	return nd.DialContext(ctx, network, addr)
}

// sshDialGuard is the ProxyOptions that routes an ssh remote's dial through
// guardedDialer. Empty for anything else: go-git's http transport reads
// ProxyOptions as a real HTTP proxy.
func sshDialGuard(rawURL string) gogittransport.ProxyOptions {
	if !IsSSHURL(rawURL) {
		return gogittransport.ProxyOptions{}
	}
	return gogittransport.ProxyOptions{URL: guardedDialScheme + "://guard"}
}
