// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sftp

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	pkgsftp "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/sandbox"
)

const (
	testUser = "feeduser"
	testPass = "s3cret"
)

// testServer is a real SSH server with a real SFTP subsystem, serving a
// temporary directory. Real rather than mocked because everything worth
// testing here lives in the protocol: host-key verification, the auth
// method offered, whether a transfer actually lands the bytes. A fake
// client would assert our own assumptions back at us.
type testServer struct {
	host        string
	port        int
	root        string // the directory the server serves
	fingerprint string // SHA256:… of its host key
}

// startSFTP brings one up and returns it. The host key is generated per
// test, so the fingerprint assertions are about this server and nothing else.
func startSFTP(t *testing.T) *testServer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("host key signer: %v", err)
	}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == testUser && string(pass) == testPass {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	cfg.AddHostKey(signer)

	root := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed by cleanup
			}
			go serveSSH(conn, cfg, root)
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return &testServer{
		host:        addr.IP.String(),
		port:        addr.Port,
		root:        root,
		fingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
	}
}

// serveSSH handles one connection: handshake, then an "sftp" subsystem
// request on a session channel. Errors are dropped rather than reported —
// a test that closes its connection mid-transfer is a case we want, and the
// assertions live on the client side.
func serveSSH(conn net.Conn, cfg *ssh.ServerConfig, root string) {
	defer conn.Close()
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			return
		}
		go func(ch ssh.Channel, reqs <-chan *ssh.Request) {
			for req := range reqs {
				ok := req.Type == "subsystem" && len(req.Payload) >= 4 &&
					string(req.Payload[4:]) == "sftp"
				if req.WantReply {
					_ = req.Reply(ok, nil)
				}
				if !ok {
					continue
				}
				srv, err := pkgsftp.NewServer(ch, pkgsftp.WithServerWorkingDirectory(root))
				if err != nil {
					_ = ch.Close()
					return
				}
				_ = srv.Serve()
				_ = ch.Close()
				return
			}
		}(ch, chReqs)
	}
}

// writeFile plants a file on the server, optionally back-dating it so the
// watermark tests have an ordering to reason about.
func (s *testServer) writeFile(t *testing.T, name, body string) {
	t.Helper()
	full := filepath.Join(s.root, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// touch sets a file's modification time, which is what the watermark keys on.
// Whole seconds, because that is all SFTP reports — and the reason the
// watermark has to remember names as well as a timestamp.
func (s *testServer) touch(t *testing.T, name string, unixSeconds int64) {
	t.Helper()
	full := filepath.Join(s.root, name)
	when := time.Unix(unixSeconds, 0)
	if err := os.Chtimes(full, when, when); err != nil {
		t.Fatal(err)
	}
}

// job is a job wired to the test server the way the engine wires a real one:
// the connection fields arrive as params (injectConnectionDefaults), with the
// per-transfer fields alongside them, plus somewhere to write.
func (s *testServer) job(t *testing.T, p map[string]any) core.Job {
	t.Helper()
	full := map[string]any{
		"host":        s.host,
		"port":        strconv.Itoa(s.port),
		"username":    testUser,
		"password":    testPass,
		"fingerprint": s.fingerprint,
		"directory":   ".",
	}
	for k, v := range p {
		full[k] = v
	}
	base := t.TempDir()
	job := core.Job{
		ID: "job-1", GraphID: "graph-1", NodeID: "node-1", Tenant: "tenant-1",
		Params:        full,
		WorkspaceRoot: filepath.Join(base, "ws"),
		ScratchRoot:   filepath.Join(base, "scratch"),
	}
	for _, d := range []string{job.WorkspaceRoot, job.ScratchRoot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return job
}

// readLocal reads a file the drops saved into the run's scratch area.
func readLocal(t *testing.T, job core.Job, ref core.Ref) string {
	t.Helper()
	rel := strings.TrimPrefix(ref.Ref, sandbox.Scheme)
	b, err := os.ReadFile(filepath.Join(job.ScratchRoot, rel))
	if err != nil {
		t.Fatalf("read saved file %q: %v", ref.Ref, err)
	}
	return string(b)
}
