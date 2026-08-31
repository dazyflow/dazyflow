// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package workspace

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/dazyflow/dazyflow/core"
	gitdrop "github.com/dazyflow/dazyflow/drops/git"
)

// The tests in mirror_test.go push over the local file transport with a nil
// auth method, which proves the refspec logic but says nothing about the leg
// a real mirror actually runs on: a private key authenticating against a git
// server over SSH. This file closes that gap with an in-process SSH server
// that verifies the client's public key and then runs git-receive-pack, so
// one test exercises the whole chain — key parsing, public-key auth,
// known_hosts host verification, receive-pack, refs landing on the far side.
//
// A real sshd would need root, a config file and a fixture host key. Go's
// x/crypto/ssh is already a dependency (the git drop's auth uses it), so the
// server is ~80 lines and leaves nothing behind.

// sshGitServer is a throwaway SSH server that serves exactly one repository.
type sshGitServer struct {
	// Addr is host:port, and HostKey is its public host key — the caller
	// needs both to build the known_hosts line the client verifies against.
	Addr    string
	HostKey gossh.PublicKey

	ln       net.Listener
	repoDir  string
	wg       sync.WaitGroup
	mu       sync.Mutex
	execCmds []string
}

// startSSHGitServer serves repoDir over SSH, accepting only clientPub.
func startSSHGitServer(t *testing.T, repoDir string, clientPub gossh.PublicKey) *sshGitServer {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available; receive-pack is what the server runs")
	}

	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}

	wantKey := clientPub.Marshal()
	cfg := &gossh.ServerConfig{
		// The point of the test: the client must present the private key
		// whose public half we were given. Anything else is refused, which
		// is what makes the wrong-key case below meaningful.
		PublicKeyCallback: func(_ gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if key.Type() == clientPub.Type() && subtleEqual(key.Marshal(), wantKey) {
				return &gossh.Permissions{}, nil
			}
			return nil, fmt.Errorf("unknown public key")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &sshGitServer{
		Addr:    ln.Addr().String(),
		HostKey: signer.PublicKey(),
		ln:      ln,
		repoDir: repoDir,
	}
	srv.wg.Add(1)
	go func() {
		defer srv.wg.Done()
		srv.acceptLoop(cfg)
	}()
	t.Cleanup(srv.Close)
	return srv
}

func (s *sshGitServer) Close() {
	_ = s.ln.Close()
	s.wg.Wait()
}

// commands returns the exec requests the server received — used to assert the
// client really asked for receive-pack rather than failing earlier.
func (s *sshGitServer) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.execCmds...)
}

func (s *sshGitServer) acceptLoop(cfg *gossh.ServerConfig) {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return // listener closed
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveConn(conn, cfg)
		}()
	}
}

func (s *sshGitServer) serveConn(conn net.Conn, cfg *gossh.ServerConfig) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	sc, chans, reqs, err := gossh.NewServerConn(conn, cfg)
	if err != nil {
		// Includes the auth failure we deliberately provoke below.
		return
	}
	defer sc.Close()
	go gossh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(gossh.UnknownChannelType, "only sessions")
			continue
		}
		ch, chReqs, err := nc.Accept()
		if err != nil {
			return
		}
		s.serveSession(ch, chReqs)
	}
}

func (s *sshGitServer) serveSession(ch gossh.Channel, reqs <-chan *gossh.Request) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		var payload struct{ Command string }
		if err := gossh.Unmarshal(req.Payload, &payload); err != nil {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			return
		}
		if req.WantReply {
			_ = req.Reply(true, nil)
		}
		s.mu.Lock()
		s.execCmds = append(s.execCmds, payload.Command)
		s.mu.Unlock()

		// A mirror push is TWO sessions: go-git first runs git-upload-pack
		// to list the remote's refs (Push needs them to work out which are
		// stale), then git-receive-pack to transfer. Serving only the
		// second is what made the first version of this test fail with
		// "unexpected EOF" — worth knowing, because it means a mirror
		// credential needs read access as well as write.
		//
		// Both run as git subcommands rather than as git-upload-pack /
		// git-receive-pack binaries, which aren't on PATH on every install.
		var sub string
		switch {
		case strings.HasPrefix(payload.Command, "git-upload-pack"):
			sub = "upload-pack"
		case strings.HasPrefix(payload.Command, "git-receive-pack"):
			sub = "receive-pack"
		default:
			sendExit(ch, 1)
			return
		}
		cmd := exec.Command("git", sub, s.repoDir)
		cmd.Stdin = ch
		cmd.Stdout = ch
		cmd.Stderr = ch.Stderr()
		code := 0
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			} else {
				code = 1
			}
		}
		sendExit(ch, code)
		return
	}
}

func sendExit(ch gossh.Channel, code int) {
	_, _ = ch.SendRequest("exit-status", false, gossh.Marshal(struct{ Status uint32 }{uint32(code)}))
	_ = ch.CloseWrite()
}

// subtleEqual is a length-safe byte compare. Not constant-time on purpose —
// this is a test fixture, and pulling in crypto/subtle would imply otherwise.
func subtleEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// testKeyPair generates an ed25519 key and returns its OpenSSH PEM (what the
// credential store holds) alongside the public half.
func testKeyPair(t *testing.T) (pemKey string, pub gossh.PublicKey) {
	t.Helper()
	rawPub, rawPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(rawPriv, "")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	pub, err = gossh.NewPublicKey(rawPub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return string(pem.EncodeToMemory(block)), pub
}

// testEncryptedKeyPair generates a passphrase-protected ed25519 key in the
// same OpenSSH PEM form the credential store holds.
func testEncryptedKeyPair(t *testing.T, passphrase string) (pemKey string, pub gossh.PublicKey) {
	t.Helper()
	rawPub, rawPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := gossh.MarshalPrivateKeyWithPassphrase(rawPriv, "", []byte(passphrase))
	if err != nil {
		t.Fatalf("marshal encrypted private key: %v", err)
	}
	pub, err = gossh.NewPublicKey(rawPub)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return string(pem.EncodeToMemory(block)), pub
}

// knownHostsLine formats a known_hosts entry for a host on a non-standard
// port, which must be bracketed: "[127.0.0.1]:2222 ssh-ed25519 AAAA…".
func knownHostsLine(addr string, key gossh.PublicKey) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = addr, "22"
	}
	target := host
	if port != "22" {
		target = "[" + host + "]:" + port
	}
	return target + " " + key.Type() + " " + base64.StdEncoding.EncodeToString(key.Marshal())
}

// seedWorkspace builds a store holding one published flow, so a push has both
// a branch and a tag to carry.
func seedWorkspace(t *testing.T) (*Store, string) {
	t.Helper()
	s, err := OpenFS(t.TempDir())
	if err != nil {
		t.Fatalf("OpenFS: %v", err)
	}
	commit, err := s.Save(core.Graph{
		ID:    "flow1",
		Nodes: []core.Node{{ID: "a", Module: "noop"}},
	}, "anna@acme.com")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.PromoteToEnvironment("flow1", PublishedEnv, commit); err != nil {
		t.Fatalf("publish: %v", err)
	}
	return s, commit
}

// TestStore_PushOverSSHWithPrivateKey is the end-to-end mirror test: a real
// private key authenticates against a real git server over SSH, and the flow
// lands on the far side. Every layer the production path uses is in play —
// gitdrop.SSHAuth for key parsing and host-key pinning, go-git's SSH
// transport, git-receive-pack — so this is what proves a configured mirror
// can actually push, rather than that we build a plausible auth object.
func TestStore_PushOverSSHWithPrivateKey(t *testing.T) {
	remote := bareRemote(t)
	keyPEM, pub := testKeyPair(t)
	srv := startSSHGitServer(t, remote, pub)

	store, commit := seedWorkspace(t)

	url := "ssh://git@" + srv.Addr + "/" + strings.TrimPrefix(remote, "/")
	auth, err := gitdrop.SSHAuth(url, keyPEM, "", knownHostsLine(srv.Addr, srv.HostKey))
	if err != nil {
		t.Fatalf("SSHAuth: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := store.Push(ctx, url, auth)
	if err != nil {
		t.Fatalf("push over SSH: %v", err)
	}
	if !res.Changed || res.Head != commit {
		t.Errorf("push result = %+v, want Changed with head %s", res, commit)
	}

	// The server really ran both halves — not, say, failed before the
	// command and let an empty push look like success.
	cmds := strings.Join(srv.commands(), " | ")
	if !strings.Contains(cmds, "git-upload-pack") || !strings.Contains(cmds, "git-receive-pack") {
		t.Errorf("server exec commands = %q, want an upload-pack (ref list) and a receive-pack (transfer)", cmds)
	}

	// And the refs landed: the flow's branch plus its published tag.
	refs := remoteRefs(t, remote)
	var branch string
	for name, hash := range refs {
		if strings.HasPrefix(name, "refs/heads/") {
			branch = hash
		}
	}
	if branch != commit {
		t.Errorf("remote branch at %q, want %q (refs: %v)", branch, commit, refs)
	}
	if _, ok := refs["refs/tags/graphs/flow1/"+PublishedEnv]; !ok {
		t.Errorf("published tag missing from the mirror (refs: %v)", refs)
	}

	// A second push with nothing new is the quiet steady state.
	res, err = store.Push(ctx, url, auth)
	if err != nil {
		t.Fatalf("second push over SSH: %v", err)
	}
	if res.Changed {
		t.Error("second push reported Changed=true; nothing had changed")
	}
}

// TestStore_PushOverSSHWithEncryptedKey covers the passphrase-protected
// deploy key, which is a realistic way to store one and a branch nothing else
// exercises end to end: SSHAuth threads the credential's passphrase into
// NewPublicKeys, and a mistake there would only ever show up as a mirror that
// can't authenticate.
func TestStore_PushOverSSHWithEncryptedKey(t *testing.T) {
	remote := bareRemote(t)
	const passphrase = "correct horse battery staple"
	keyPEM, pub := testEncryptedKeyPair(t, passphrase)
	srv := startSSHGitServer(t, remote, pub)
	store, commit := seedWorkspace(t)

	url := "ssh://git@" + srv.Addr + "/" + strings.TrimPrefix(remote, "/")
	auth, err := gitdrop.SSHAuth(url, keyPEM, passphrase, knownHostsLine(srv.Addr, srv.HostKey))
	if err != nil {
		t.Fatalf("SSHAuth with an encrypted key: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := store.Push(ctx, url, auth); err != nil {
		t.Fatalf("push with an encrypted key: %v", err)
	}
	var branch string
	for name, hash := range remoteRefs(t, remote) {
		if strings.HasPrefix(name, "refs/heads/") {
			branch = hash
		}
	}
	if branch != commit {
		t.Errorf("remote branch at %q, want %q", branch, commit)
	}

	// The wrong passphrase must fail at key parsing, with a message that
	// points at the passphrase rather than at the key being malformed.
	if _, err := gitdrop.SSHAuth(url, keyPEM, "wrong", knownHostsLine(srv.Addr, srv.HostKey)); err == nil {
		t.Error("SSHAuth accepted the wrong passphrase")
	} else if !strings.Contains(err.Error(), "passphrase") {
		t.Errorf("error = %q, want it to mention the passphrase", err)
	}
}

// TestStore_PushOverSSHRejectsWrongKey — the server accepts one key, and we
// present another. This is what a revoked or mistyped deploy key looks like,
// and the failure must surface rather than being swallowed as a no-op
// "nothing to push".
func TestStore_PushOverSSHRejectsWrongKey(t *testing.T) {
	remote := bareRemote(t)
	_, authorizedPub := testKeyPair(t)
	srv := startSSHGitServer(t, remote, authorizedPub)

	otherKeyPEM, _ := testKeyPair(t) // not the key the server knows
	store, _ := seedWorkspace(t)

	url := "ssh://git@" + srv.Addr + "/" + strings.TrimPrefix(remote, "/")
	auth, err := gitdrop.SSHAuth(url, otherKeyPEM, "", knownHostsLine(srv.Addr, srv.HostKey))
	if err != nil {
		t.Fatalf("SSHAuth: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = store.Push(ctx, url, auth)
	if err == nil {
		t.Fatal("push with an unauthorized key succeeded; it must fail")
	}
	// The reason has to reach the user — this string is what the mirror panel
	// shows and what tells someone their key isn't installed on the remote.
	// Asserted specifically so the test can't pass on some unrelated failure
	// (a wrong path, a closed port) that would also produce an error.
	if !strings.Contains(err.Error(), "unable to authenticate") {
		t.Errorf("error = %q, want it to name the authentication failure", err)
	}
	if refs := remoteRefs(t, remote); len(refs) != 0 {
		t.Errorf("a rejected push must leave the remote empty, got %v", refs)
	}
}

// TestStore_PushOverSSHVerifiesHostKey is the MITM guard. The client is given
// a known_hosts line for a DIFFERENT host key, so the handshake must fail —
// the mirror never falls back to accepting whatever key it is offered.
func TestStore_PushOverSSHVerifiesHostKey(t *testing.T) {
	remote := bareRemote(t)
	keyPEM, pub := testKeyPair(t)
	srv := startSSHGitServer(t, remote, pub)
	store, _ := seedWorkspace(t)

	// A known_hosts entry naming the right host but the wrong key.
	_, impostor := testKeyPair(t)
	url := "ssh://git@" + srv.Addr + "/" + strings.TrimPrefix(remote, "/")
	auth, err := gitdrop.SSHAuth(url, keyPEM, "", knownHostsLine(srv.Addr, impostor))
	if err != nil {
		t.Fatalf("SSHAuth: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = store.Push(ctx, url, auth)
	if err == nil {
		t.Fatal("push accepted a host key that known_hosts did not list")
	}
	// Specifically a host-key mismatch, not just "some error" — otherwise
	// this test would pass even if the push failed for an unrelated reason
	// and the MITM guard had quietly stopped working.
	if !strings.Contains(err.Error(), "knownhosts: key mismatch") {
		t.Errorf("error = %q, want a known_hosts key mismatch", err)
	}
	if refs := remoteRefs(t, remote); len(refs) != 0 {
		t.Errorf("a failed handshake must leave the remote empty, got %v", refs)
	}
}
