package daemon_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	controlpb "git.sr.ht/~klahr/hazy-flow/api/gen/control"
	"git.sr.ht/~klahr/hazy-flow/auth"
	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/daemon"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/engine/jobstore"
	"git.sr.ht/~klahr/hazy-flow/workspace"
)

// testCerts holds an in-memory PKI tree: one CA signs both a server cert
// (SAN: localhost, 127.0.0.1) and a client cert.
type testCerts struct {
	caPEM         []byte
	serverCertPEM []byte
	serverKeyPEM  []byte
	clientCertPEM []byte
	clientKeyPEM  []byte
}

func makeTestCerts(t *testing.T) *testCerts {
	t.Helper()
	now := time.Now()

	// --- CA ---
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "hazyflow-test-ca"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caBytes, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("ca create: %v", err)
	}
	caCert, _ := x509.ParseCertificate(caBytes)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caBytes})

	// --- Server cert ---
	serverKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serverTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "hazyflow-test-server"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost", "hzd.test"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	serverBytes, _ := x509.CreateCertificate(rand.Reader, serverTmpl, caCert, &serverKey.PublicKey, caKey)
	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverBytes})
	serverKeyPEM := marshalECKey(t, serverKey)

	// --- Client cert ---
	clientKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	clientTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "hazyflow-test-client"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientBytes, _ := x509.CreateCertificate(rand.Reader, clientTmpl, caCert, &clientKey.PublicKey, caKey)
	clientCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientBytes})
	clientKeyPEM := marshalECKey(t, clientKey)

	return &testCerts{
		caPEM:         caPEM,
		serverCertPEM: serverCertPEM,
		serverKeyPEM:  serverKeyPEM,
		clientCertPEM: clientCertPEM,
		clientKeyPEM:  clientKeyPEM,
	}
}

func marshalECKey(t *testing.T, k *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatalf("marshal ec key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

// startTLSServer wires the same gRPC + Service + Worker stack the other
// tests use, but listening on a real TCP socket with the supplied TLS
// config. Returns the listen address and a stop func.
func startTLSServer(t *testing.T, tlsCfg *tls.Config) (string, string, func()) {
	t.Helper()
	ks := auth.NewMemKeyStore()
	role := core.Role{Name: "editor", Permissions: []core.Permission{
		core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
	}}
	_, apiKey, err := auth.IssueAPIKey(ks, t.Context(), "k", "acme", "ws1", "u", []core.Role{role}, nil)
	if err != nil {
		t.Fatalf("issue key: %v", err)
	}

	ws, _ := workspace.OpenFS("")
	jobs := jobstore.NewMemory()
	bus := daemon.NewMemoryBus()
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Auth:       auth.Chain{&auth.APIKeyAuthenticator{Store: ks}},
		Workspaces: daemon.MapWorkspaces{"acme/ws1": ws},
		Jobs:       jobs,
		Engine:     eng,
		Bus:        bus,
	}

	wctx, cancel := context.WithCancel(context.Background())
	w := daemon.NewWorker(daemon.WorkerConfig{
		ID: "w", PollInterval: 5 * time.Millisecond,
		LeaseDuration: 5 * time.Second, LeaseRenewEvery: 1 * time.Second,
	}, jobs, eng, bus)
	go func() { _ = w.Run(wctx) }()

	unary, stream := daemon.AuthInterceptors(svc.Auth)
	srv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsCfg)),
		grpc.UnaryInterceptor(unary),
		grpc.StreamInterceptor(stream),
	)
	daemon.RegisterGRPC(srv, svc)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(lis)

	stop := func() {
		srv.Stop()
		cancel()
	}
	return lis.Addr().String(), apiKey, stop
}

func TestMTLS_HappyPath(t *testing.T) {
	certs := makeTestCerts(t)
	serverCfg, err := daemon.ServerConfigFromPEM(certs.serverCertPEM, certs.serverKeyPEM, certs.caPEM)
	if err != nil {
		t.Fatalf("server config: %v", err)
	}
	addr, apiKey, stop := startTLSServer(t, serverCfg)
	defer stop()

	clientCfg, err := daemon.ClientConfigFromPEM(certs.clientCertPEM, certs.clientKeyPEM, certs.caPEM, "localhost")
	if err != nil {
		t.Fatalf("client config: %v", err)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(clientCfg)))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+apiKey)

	// Drive a real RPC to confirm the mTLS handshake actually flowed.
	resp, err := controlpb.NewDropServiceClient(conn).ListDrops(ctx, &controlpb.ListDropsRequest{})
	if err != nil {
		t.Fatalf("ListModules: %v", err)
	}
	if len(resp.Drops) == 0 {
		t.Error("ListDrops returned no drops")
	}
}

func TestMTLS_ClientWithoutCertRejected(t *testing.T) {
	certs := makeTestCerts(t)
	serverCfg, _ := daemon.ServerConfigFromPEM(certs.serverCertPEM, certs.serverKeyPEM, certs.caPEM)
	addr, _, stop := startTLSServer(t, serverCfg)
	defer stop()

	// Client trusts the server's CA but presents no client cert.
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(certs.caPEM)
	clientCfg := &tls.Config{
		RootCAs:    pool,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(clientCfg)))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, err = controlpb.NewDropServiceClient(conn).ListDrops(ctx, &controlpb.ListDropsRequest{})
	if err == nil {
		t.Fatal("RPC succeeded without a client cert")
	}
	// Surfaces as either Unavailable (handshake failure) or a TLS error
	// in the connection state. We just want the call to fail.
}

func TestMTLS_WrongCARejected(t *testing.T) {
	serverCerts := makeTestCerts(t)
	// A whole second PKI tree the server doesn't trust.
	otherCerts := makeTestCerts(t)

	serverCfg, _ := daemon.ServerConfigFromPEM(
		serverCerts.serverCertPEM, serverCerts.serverKeyPEM, serverCerts.caPEM)
	addr, _, stop := startTLSServer(t, serverCfg)
	defer stop()

	// Client uses certs signed by a CA the server doesn't trust.
	clientCfg, _ := daemon.ClientConfigFromPEM(
		otherCerts.clientCertPEM, otherCerts.clientKeyPEM, serverCerts.caPEM, "localhost")
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(clientCfg)))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, err = controlpb.NewDropServiceClient(conn).ListDrops(ctx, &controlpb.ListDropsRequest{})
	if err == nil {
		t.Fatal("RPC succeeded with untrusted client cert")
	}
}

func TestMTLS_InsecureClientCannotTalkToTLSServer(t *testing.T) {
	certs := makeTestCerts(t)
	serverCfg, _ := daemon.ServerConfigFromPEM(certs.serverCertPEM, certs.serverKeyPEM, certs.caPEM)
	addr, _, stop := startTLSServer(t, serverCfg)
	defer stop()

	// Plain insecure client connecting to a TLS server.
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, err = controlpb.NewDropServiceClient(conn).ListDrops(ctx, &controlpb.ListDropsRequest{})
	if err == nil {
		t.Fatal("insecure client succeeded against TLS server")
	}
	// gRPC surfaces this as Unavailable / connection error.
	if st, ok := status.FromError(err); ok && st.Code() == codes.OK {
		t.Fatal("RPC reported OK status with err set")
	}
}

func TestMTLS_FileLoaderRoundTrip(t *testing.T) {
	// Verify TLSFiles can read PEM files from disk and produce a usable
	// pair of configs.
	certs := makeTestCerts(t)
	dir := t.TempDir()
	mustWrite := func(name string, data []byte) string {
		path := dir + "/" + name
		if err := writeFile(path, data); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	files := daemon.TLSFiles{
		CertFile: mustWrite("server.crt", certs.serverCertPEM),
		KeyFile:  mustWrite("server.key", certs.serverKeyPEM),
		CAFile:   mustWrite("ca.crt", certs.caPEM),
	}
	if _, err := files.LoadServerConfig(); err != nil {
		t.Fatalf("LoadServerConfig: %v", err)
	}

	clientFiles := daemon.TLSFiles{
		CertFile: mustWrite("client.crt", certs.clientCertPEM),
		KeyFile:  mustWrite("client.key", certs.clientKeyPEM),
		CAFile:   files.CAFile,
	}
	if _, err := clientFiles.LoadClientConfig("localhost"); err != nil {
		t.Fatalf("LoadClientConfig: %v", err)
	}
}

func TestMTLS_LoaderRejectsBadInput(t *testing.T) {
	// Empty files struct
	_, err := daemon.TLSFiles{}.LoadServerConfig()
	if err == nil {
		t.Error("empty server config should error")
	}
	_, err = daemon.TLSFiles{}.LoadClientConfig("")
	if err == nil {
		t.Error("empty client config should error")
	}
	// Bad CA PEM
	dir := t.TempDir()
	bad := dir + "/ca.crt"
	_ = writeFile(bad, []byte("not a pem"))
	certs := makeTestCerts(t)
	good := dir + "/cert.crt"
	goodKey := dir + "/cert.key"
	_ = writeFile(good, certs.serverCertPEM)
	_ = writeFile(goodKey, certs.serverKeyPEM)
	_, err = daemon.TLSFiles{CertFile: good, KeyFile: goodKey, CAFile: bad}.LoadServerConfig()
	if err == nil || !strings.Contains(err.Error(), "no usable certificates") {
		t.Errorf("bad CA: err = %v", err)
	}
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
