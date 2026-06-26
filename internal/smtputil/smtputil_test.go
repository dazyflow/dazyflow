package smtputil

import (
	"bufio"
	"context"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

// smtpScript configures the scripted server's behavior.
type smtpScript struct {
	withAuth  bool   // advertise AUTH PLAIN in EHLO
	authFail  bool   // reject AUTH with 535
	rejectCmd string // command prefix to reject with 550 (e.g. "MAIL", "RCPT")
}

// scriptedSMTP starts a minimal plaintext SMTP server that walks one
// client through the MAIL/RCPT/DATA/QUIT dance. The returned addr is the
// listener's host:port; it serves exactly one connection then stops.
func scriptedSMTP(t *testing.T, sc smtpScript) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }

		w("220 test ESMTP")
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if inData {
				if line == "." {
					inData = false
					w("250 2.0.0 queued")
				}
				continue
			}
			if sc.rejectCmd != "" && strings.HasPrefix(line, sc.rejectCmd) {
				w("550 5.0.0 rejected")
				continue
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				w("250-test")
				if sc.withAuth {
					w("250-AUTH PLAIN")
				}
				w("250 SMTPUTF8")
			case strings.HasPrefix(line, "HELO"):
				w("250 test")
			case strings.HasPrefix(line, "AUTH"):
				if sc.authFail {
					w("535 5.7.8 bad credentials")
				} else {
					w("235 2.7.0 accepted")
				}
			case strings.HasPrefix(line, "MAIL"):
				w("250 2.1.0 ok")
			case strings.HasPrefix(line, "RCPT"):
				w("250 2.1.5 ok")
			case strings.HasPrefix(line, "DATA"):
				inData = true
				w("354 end with .")
			case strings.HasPrefix(line, "QUIT"):
				w("221 2.0.0 bye")
				return
			default:
				w("250 ok")
			}
		}
	}()
	return ln.Addr().String()
}

func TestSend_PlainHappyPath(t *testing.T) {
	addr := scriptedSMTP(t, smtpScript{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Send(ctx, addr, "127.0.0.1", "none", nil, "from@x.test", []string{"to@x.test"}, []byte("Subject: hi\r\n\r\nbody"))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
}

func TestSend_WithAuth(t *testing.T) {
	addr := scriptedSMTP(t, smtpScript{withAuth: true})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	auth := smtp.PlainAuth("", "user", "pass", "127.0.0.1")
	err := Send(ctx, addr, "127.0.0.1", "none", auth, "from@x.test", []string{"to@x.test"}, []byte("body"))
	if err != nil {
		t.Fatalf("Send with auth: %v", err)
	}
}

func TestSend_AuthRejected(t *testing.T) {
	addr := scriptedSMTP(t, smtpScript{withAuth: true, authFail: true})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	auth := smtp.PlainAuth("", "user", "bad", "127.0.0.1")
	err := Send(ctx, addr, "127.0.0.1", "none", auth, "from@x.test", []string{"to@x.test"}, []byte("body"))
	if err == nil || !strings.Contains(err.Error(), "auth") {
		t.Fatalf("Send with bad auth = %v, want auth error", err)
	}
}

func TestSend_MailFromRejected(t *testing.T) {
	addr := scriptedSMTP(t, smtpScript{rejectCmd: "MAIL"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Send(ctx, addr, "127.0.0.1", "none", nil, "from@x.test", []string{"to@x.test"}, []byte("body"))
	if err == nil || !strings.Contains(err.Error(), "mail from") {
		t.Fatalf("Send = %v, want mail-from error", err)
	}
}

func TestSend_RcptRejected(t *testing.T) {
	addr := scriptedSMTP(t, smtpScript{rejectCmd: "RCPT"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Send(ctx, addr, "127.0.0.1", "none", nil, "from@x.test", []string{"to@x.test"}, []byte("body"))
	if err == nil || !strings.Contains(err.Error(), "rcpt") {
		t.Fatalf("Send = %v, want rcpt error", err)
	}
}

func TestVerify_HappyPath(t *testing.T) {
	// mode "starttls" against a server that does NOT advertise STARTTLS
	// exercises the starttls branch's skip path (extension absent → no
	// upgrade attempted).
	addr := scriptedSMTP(t, smtpScript{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Verify(ctx, addr, "127.0.0.1", "starttls", nil); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerify_NoDeadlineFallback(t *testing.T) {
	// A context without a deadline exercises dial's 30s fallback branch.
	addr := scriptedSMTP(t, smtpScript{})
	if err := Verify(context.Background(), addr, "127.0.0.1", "none", nil); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestSend_DialError(t *testing.T) {
	// Reserve a port and immediately close it so the dial is refused.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := Send(ctx, addr, "127.0.0.1", "none", nil, "f@x", []string{"t@x"}, []byte("b")); err == nil {
		t.Error("expected dial error to a closed port")
	}
	if err := Verify(ctx, addr, "127.0.0.1", "none", nil); err == nil {
		t.Error("expected Verify dial error to a closed port")
	}
}

func TestSend_ImplicitTLSDialError(t *testing.T) {
	// The implicit-TLS branch dials with a TLS handshake; a plain closed
	// port still fails at dial, exercising the mode=="implicit" path.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := Send(ctx, addr, "127.0.0.1", "implicit", nil, "f@x", []string{"t@x"}, []byte("b")); err == nil {
		t.Error("expected implicit-TLS dial error")
	}
}
