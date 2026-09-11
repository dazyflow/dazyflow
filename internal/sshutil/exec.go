// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sshutil

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Running one command on a server, and getting back what a shell would have
// shown you: stdout, stderr, and the exit status.
//
// There is no TTY. That is the right default for automation — a TTY interleaves
// the two streams into one and fills them with escape codes — but it means an
// interactive prompt has nobody to answer it: `sudo` without NOPASSWD waits for
// a password until the step's deadline. The drop's description says so, because
// the symptom (a step that hangs, then times out) does not point at the cause.

type RunRequest struct {
	Command string

	// Set in the shell before Command runs, not through the SSH protocol's own
	// env request: sshd only forwards variables named in its AcceptEnv, which is
	// empty in a default config, so Setenv would silently deliver nothing on most
	// servers. An export line always works and fails visibly when it doesn't.
	Env map[string]string

	WorkingDir string

	Stdin string

	// Per stream, and counted in bytes of output rather than lines: a runaway
	// command is stopped from filling the run record either way, and a byte
	// budget is the one a reader can reason about against a quota.
	MaxOutput int

	// Called with each complete stdout line as it arrives, for the step console.
	// Lines still accumulate into Stdout — this streams a copy, it does not
	// replace the result.
	OnStdout func(string)
}

type RunResult struct {
	ExitCode int
	Stdout   string
	Stderr   string

	StdoutTruncated bool
	StderrTruncated bool

	// Set when the command died on a signal instead of exiting — "KILL" is the
	// out-of-memory killer's fingerprint and worth telling the operator apart
	// from an exit code the script chose.
	Signal string
}

// Run executes one command and waits for it to finish.
//
// A command that runs and fails is NOT an error here: a non-zero exit lands in
// RunResult.ExitCode and the caller decides what it means, because only the
// caller knows whether 2 is "broken" or "nothing to do today". An error return
// means the command never ran, or we never learned how it ended.
func (c *SSHClient) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	var res RunResult
	if c == nil || c.ssh == nil {
		return res, errors.New("not connected")
	}
	if strings.TrimSpace(req.Command) == "" {
		return res, errors.New("no command to run")
	}

	session, err := c.ssh.NewSession()
	if err != nil {
		return res, fmt.Errorf("signed in, but couldn't start a session: %w", err)
	}
	defer func() { _ = session.Close() }()

	// The connection carries a deadline from the dial; a command legitimately
	// outlives it, so the context is what bounds this from here on.
	_ = c.conn.SetDeadline(time.Time{})

	out := &capWriter{limit: req.MaxOutput, onLine: req.OnStdout}
	errw := &capWriter{limit: req.MaxOutput}
	session.Stdout = out
	session.Stderr = errw
	if req.Stdin != "" {
		session.Stdin = strings.NewReader(req.Stdin)
	}

	// Closing the session is what unblocks Wait; the deadline on the underlying
	// connection is the backstop for a server that has stopped reading.
	done := make(chan struct{})
	var closeOnce sync.Once
	defer closeOnce.Do(func() { close(done) })
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Signal(ssh.SIGTERM)
			_ = session.Close()
			_ = c.conn.SetDeadline(time.Now())
		case <-done:
		}
	}()

	runErr := session.Run(shellScript(req))

	res.Stdout, res.StdoutTruncated = out.result()
	res.Stderr, res.StderrTruncated = errw.result()

	if ctx.Err() != nil {
		return res, fmt.Errorf("the command was still running after the time allowed — raise the timeout, or have the command return sooner (%w)", ctx.Err())
	}

	switch {
	case runErr == nil:
		return res, nil
	case isExit(runErr, &res):
		return res, nil
	default:
		var missing *ssh.ExitMissingError
		if errors.As(runErr, &missing) {
			return res, errors.New("the server closed the connection without saying how the command ended — it may have been killed, or the connection dropped mid-run")
		}
		return res, fmt.Errorf("couldn't run the command: %w", runErr)
	}
}

// isExit reports whether err is the command's own exit status rather than a
// failure to run it, filling in the result when it is.
func isExit(err error, res *RunResult) bool {
	var ee *ssh.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	res.ExitCode = ee.ExitStatus()
	res.Signal = ee.Signal()
	// A signalled command reports status 0 on some servers; saying it exited
	// cleanly when the OOM killer took it would be a lie the flow acts on.
	if res.Signal != "" && res.ExitCode == 0 {
		res.ExitCode = 128
	}
	return true
}

// shellScript wraps the command in the working directory and environment the
// step asked for. The result is fed to the account's login shell, so it is
// POSIX sh — a Windows OpenSSH host running cmd will not understand the export
// lines, which is a limitation the drop's description states rather than one
// this tries to paper over.
func shellScript(req RunRequest) string {
	var b strings.Builder
	for _, k := range sortedEnvKeys(req.Env) {
		b.WriteString("export ")
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(shellQuote(req.Env[k]))
		b.WriteString("\n")
	}
	if dir := strings.TrimSpace(req.WorkingDir); dir != "" {
		// || exit: carrying on in the home directory because a cd failed is how
		// a backup ends up written somewhere nobody looks.
		b.WriteString("cd ")
		b.WriteString(shellQuote(dir))
		b.WriteString(" || exit 1\n")
	}
	b.WriteString(req.Command)
	return b.String()
}

func sortedEnvKeys(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		if validEnvName(k) {
			keys = append(keys, k)
		}
	}
	// Sorted so the same step produces the same script every run — a script that
	// differs only in line order is noise in a diff of two runs.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// validEnvName keeps a name that would otherwise let a value out of its quotes
// from reaching the shell at all: "A=1; rm -rf /" is not a variable name.
func validEnvName(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// shellQuote renders s as one single-quoted POSIX shell word. Single quotes
// suspend every expansion the shell does, so the only character needing care is
// the closing quote itself: end the string, add an escaped quote, start again.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// capWriter accumulates up to limit bytes and counts the rest away, so a command
// that prints a gigabyte costs a gigabyte of transfer but not of memory.
type capWriter struct {
	limit   int
	onLine  func(string)
	mu      sync.Mutex
	buf     []byte
	dropped bool
	partial []byte
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.limit > 0 {
		if room := w.limit - len(w.buf); room <= 0 {
			w.dropped = true
		} else if len(p) > room {
			w.buf = append(w.buf, p[:room]...)
			w.dropped = true
		} else {
			w.buf = append(w.buf, p...)
		}
	} else {
		w.buf = append(w.buf, p...)
	}
	if w.onLine != nil {
		w.emitLines(p)
	}
	// Always the full length: a short write makes the ssh library treat a
	// truncated capture as a broken stream and tear down the session.
	return len(p), nil
}

// emitLines forwards complete lines only, holding the tail until its newline
// arrives — a console line that appears in two halves reads as two lines.
func (w *capWriter) emitLines(p []byte) {
	w.partial = append(w.partial, p...)
	for {
		i := indexByte(w.partial, '\n')
		if i < 0 {
			return
		}
		line := strings.TrimRight(string(w.partial[:i]), "\r")
		w.partial = w.partial[i+1:]
		w.onLine(line)
	}
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func (w *capWriter) result() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Whatever never got its newline is still output the operator needs to see.
	if w.onLine != nil && len(w.partial) > 0 {
		w.onLine(strings.TrimRight(string(w.partial), "\r"))
		w.partial = nil
	}
	return string(w.buf), w.dropped
}

// VerifySSH is the "Test connection" probe for a credential used to run
// commands. It deliberately runs something rather than only signing in: an
// account restricted to SFTP (ForceCommand internal-sftp) authenticates
// perfectly and then refuses every command, which is worth finding on the
// credentials page rather than in a flow at 03:00.
func VerifySSH(ctx context.Context, cfg Config) error {
	c, err := DialSSH(ctx, cfg)
	if err != nil {
		return err
	}
	defer c.Close()

	res, err := c.Run(ctx, RunRequest{Command: "echo dazyflow", MaxOutput: 4096})
	if err != nil {
		return fmt.Errorf("signed in to %s, but it wouldn't run a command — the account may be restricted to file transfer (%w)", cfg.Host, err)
	}
	// A non-zero status still proves a shell ran, which is all this checks.
	_ = res
	return nil
}
