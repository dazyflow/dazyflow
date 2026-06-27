// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// TLSFiles holds the file paths for a TLS identity (own cert + key) and
// the CA bundle used to verify the peer. Either side of the connection
// uses the same struct: dzd's server config and dzctl's client config
// both point at PEM files on disk.
type TLSFiles struct {
	CertFile string
	KeyFile  string
	CAFile   string
}

// LoadServerConfig produces a *tls.Config suitable for a gRPC server
// that requires mTLS. Clients must present a certificate signed by one
// of the CAs in CAFile.
func (f TLSFiles) LoadServerConfig() (*tls.Config, error) {
	cert, err := loadKeyPair(f.CertFile, f.KeyFile, "server")
	if err != nil {
		return nil, err
	}
	pool, err := loadCAPool(f.CAFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// LoadClientConfig produces a *tls.Config suitable for a gRPC client. The
// CA bundle verifies the server's cert; the local cert/key are presented
// to the server for mTLS. ServerName overrides SNI/hostname verification
// — needed when dialling something whose cert SAN is not the DNS name in
// the endpoint (e.g. tests using bufconn).
func (f TLSFiles) LoadClientConfig(serverName string) (*tls.Config, error) {
	cert, err := loadKeyPair(f.CertFile, f.KeyFile, "client")
	if err != nil {
		return nil, err
	}
	pool, err := loadCAPool(f.CAFile)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	}
	if serverName != "" {
		cfg.ServerName = serverName
	}
	return cfg, nil
}

// ServerConfigFromPEM is the test-friendly variant: caller hands over
// already-decoded PEM bytes rather than file paths.
func ServerConfigFromPEM(certPEM, keyPEM, caPEM []byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("server keypair: %w", err)
	}
	pool, err := caPoolFromPEM(caPEM)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// ClientConfigFromPEM mirrors ServerConfigFromPEM on the client side.
func ClientConfigFromPEM(certPEM, keyPEM, caPEM []byte, serverName string) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("client keypair: %w", err)
	}
	pool, err := caPoolFromPEM(caPEM)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS13,
	}
	if serverName != "" {
		cfg.ServerName = serverName
	}
	return cfg, nil
}

func loadKeyPair(certFile, keyFile, role string) (tls.Certificate, error) {
	if certFile == "" || keyFile == "" {
		return tls.Certificate{}, fmt.Errorf("%s tls: cert and key required", role)
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("%s tls: load keypair (%s, %s): %w", role, certFile, keyFile, err)
	}
	return cert, nil
}

func loadCAPool(caFile string) (*x509.CertPool, error) {
	if caFile == "" {
		return nil, errors.New("tls: CA file required")
	}
	data, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("tls: read CA %q: %w", caFile, err)
	}
	return caPoolFromPEM(data)
}

func caPoolFromPEM(data []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, errors.New("tls: CA PEM contained no usable certificates")
	}
	return pool, nil
}
