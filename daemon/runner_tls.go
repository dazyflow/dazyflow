// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"time"

	"git.sr.ht/~klahr/dazyflow/engine"
)

// Mutual TLS for runners, both directions pinned.
//
// engine/remote.go has always had a RemoteTLS field and a comment pointing at
// "daemon.TLSFiles for the standard loader". No such loader existed — the
// comment described an intention. This is it, with one deliberate difference
// from what that comment implied: nothing here reads PEM off disk. The material
// comes from the tenant's registration, so a multi-tenant daemon does not need
// a filesystem layout to keep one org's key away from another's.
//
// Neither side trusts a public CA:
//
//	The daemon verifies the runner against exactly the certificate the org
//	registered (RootCAs holds that one certificate, nothing else).
//
//	The runner verifies the daemon against the client certificate the org
//	issued and installed on its side.
//
// For two parties who already know each other, pinning is stricter than
// chaining: no public CA can be persuaded to mint a certificate for the org's
// hostname and get in.

// tlsKeyPair parses a client certificate and its private key.
//
// Split out so registration can validate the pair while an admin is still
// looking at the form. A bad pair discovered at connect time is a failure hours
// later, inside a run, blamed on the runner.
func tlsKeyPair(certPEM, keyPEM []byte) (tls.Certificate, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("client certificate and key do not form a usable pair: %w", err)
	}
	return cert, nil
}

// runnerTLSConfig builds the client-side TLS configuration for one runner.
func runnerTLSConfig(r Runner, clientKeyPEM []byte) (*tls.Config, error) {
	cert, err := tlsKeyPair(r.ClientCertPEM, clientKeyPEM)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(r.ServerCAPEM) {
		return nil, fmt.Errorf("the runner's certificate is not valid PEM")
	}
	// The pinned certificate has to be valid FOR the host being dialled, so
	// the SAN in it must match. Deriving ServerName from the endpoint rather
	// than accepting one separately keeps that a property of the address the
	// admin typed, with nothing to get out of step.
	host := r.Endpoint
	if h, _, splitErr := net.SplitHostPort(r.Endpoint); splitErr == nil {
		host = h
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   host,
		// 1.2 rather than 1.3 as the floor. Every current gRPC stack negotiates
		// 1.3 with a Go server anyway, so the practical security is the same;
		// what the lower floor buys is that an org running an older runtime
		// gets a working connection instead of a handshake error that reads
		// like a certificate problem.
		MinVersion: tls.VersionTLS12,
	}, nil
}

// Descriptor turns a stored runner into something the engine can register.
//
// This is the one place the private key is decrypted, and it is not retained:
// it goes into a tls.Certificate and the PEM is dropped when this returns.
func (rs *Runners) Descriptor(ctx context.Context, tenant, name string) (engine.RemoteDescriptor, error) {
	r, err := rs.Store.Get(ctx, tenant, name)
	if err != nil {
		return engine.RemoteDescriptor{}, err
	}
	keyPEM, err := rs.clientKey(ctx, tenant, name)
	if err != nil {
		return engine.RemoteDescriptor{}, fmt.Errorf("runner %q: read client key: %w", name, err)
	}
	return rs.descriptorFor(r, keyPEM)
}

func (rs *Runners) descriptorFor(r Runner, keyPEM []byte) (engine.RemoteDescriptor, error) {
	cfg, err := runnerTLSConfig(r, keyPEM)
	if err != nil {
		return engine.RemoteDescriptor{}, fmt.Errorf("runner %q: %w", r.Name, err)
	}
	return engine.RemoteDescriptor{
		ID:          r.Name,
		Tenant:      r.Tenant,
		Endpoint:    r.Endpoint,
		TLS:         &engine.RemoteTLS{Config: cfg},
		RecvTimeout: r.RecvTimeout,
	}, nil
}

// expiringWithin reports whether the client certificate expires before now+d.
// Used by the admin list so an operator hears about it while there is still
// time to rotate, rather than from a run that failed.
func (r Runner) expiringWithin(d time.Duration) bool {
	return !r.NotAfter.IsZero() && time.Until(r.NotAfter) < d
}

// Probe dials a runner using material that has not been stored, and reports
// the drops it declares.
//
// A throwaway catalog, closed on the way out: a probe must not leave the live
// catalog holding a connection to something the admin may be about to change
// or abandon. It is also why this takes a Runner rather than a name — testing
// happens before saving, not after.
func (rs *Runners) Probe(ctx context.Context, r Runner) ([]string, error) {
	if len(r.ClientKeyPEM) == 0 {
		return nil, fmt.Errorf("a client key is required to test the connection")
	}
	desc, err := rs.descriptorFor(r, r.ClientKeyPEM)
	if err != nil {
		return nil, err
	}
	cat := engine.NewRemoteCatalog()
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d > 0 {
			cat.DialTimeout = d
		}
	}
	defer func() { _ = cat.Close() }()
	if err := cat.Register(desc); err != nil {
		return nil, err
	}
	return cat.DropsFor(r.Tenant, r.Name), nil
}
