// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package ssh

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/dropstest"
	"github.com/dazyflow/dazyflow/drops/sshcreds"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/sshutil"
)

func TestMain(m *testing.M) { dropstest.EgressTestMain(m) }

// The assertion this step owes above all others: it dials an address and then
// authenticates, so an unguarded one would offer a tenant-chosen host the
// server password or a signature from the private key.
func TestSSHDial_SSRFGuardBlocksPrivate(t *testing.T) {
	dropstest.AssertSSRFBlocked(t, func() error {
		_, err := sshutil.DialSSH(context.Background(), sshutil.Config{
			Host: "127.0.0.1", Port: 22, Username: "u", Password: "p",
			Fingerprint: "SHA256:whatever",
		})
		return err
	})
}

func TestSSHRun_Registered(t *testing.T) {
	m, ok := engine.Default.Manifests()["ssh_run"]
	if !ok {
		t.Fatal("ssh_run is not registered")
	}
	if m.Integration != integration || m.BrandLogo != "/brands/ssh.svg" {
		t.Errorf("manifest identity wrong: %+v", m)
	}
	// Running a command changes the server, so a resumed run must not silently
	// do it twice.
	if m.Idempotent {
		t.Error("ssh_run claims to be idempotent")
	}
}

func withLookup(t *testing.T, fn sshcreds.Lookup) {
	t.Helper()
	sshcreds.SetLookup(fn)
	t.Cleanup(func() { sshcreds.SetLookup(nil) })
}

func run(t *testing.T, job core.Job) core.Result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := executeSSHRun(ctx, job, nil)
	if err != nil {
		t.Fatalf("executeSSHRun returned a transport error: %v", err)
	}
	return res
}

func errCode(t *testing.T, res core.Result) string {
	t.Helper()
	if res.Status == core.StatusOK {
		t.Fatalf("step succeeded, want a failure (%+v)", res)
	}
	return res.Error.Code
}

// Every guard below runs BEFORE anything is dialled: a step that is misconfigured
// should say so rather than open a connection to find out.
func TestSSHRun_RefusesBeforeDialling(t *testing.T) {
	cfg := sshutil.Config{Host: "example.com", Port: 22, Username: "u", Password: "p"}
	withLookup(t, func(context.Context, string) (sshutil.Config, error) { return cfg, nil })

	for _, tc := range []struct {
		name string
		job  core.Job
		want string
	}{
		{
			"no command",
			core.Job{ID: "j", Params: map[string]any{"account": "web-1"}},
			"no_command",
		},
		{
			"blank command",
			core.Job{ID: "j", Params: map[string]any{"account": "web-1", "command": "   "}},
			"no_command",
		},
		{
			"an env name that is really a command",
			core.Job{ID: "j", Params: map[string]any{
				"account": "web-1", "command": "true",
				"env": map[string]any{"A=1; rm -rf /": "x"},
			}},
			"bad_env",
		},
		{
			"an unknown answer to the non-zero question",
			core.Job{ID: "j", Params: map[string]any{
				"account": "web-1", "command": "true", "on_nonzero_exit": "maybe",
			}},
			"bad_param",
		},
		{
			"no server chosen",
			core.Job{ID: "j", Params: map[string]any{"command": "uptime"}},
			"no_server",
		},
		{
			"blank server",
			core.Job{ID: "j", Params: map[string]any{"account": "  ", "command": "uptime"}},
			"no_server",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := errCode(t, run(t, tc.job)); got != tc.want {
				t.Errorf("error code = %q, want %q", got, tc.want)
			}
		})
	}
}

// A daemon with no credential store, and an account nobody created, are the
// same thing to the operator: the server is not set up.
func TestSSHRun_UnconfiguredServerSaysSo(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lookup sshcreds.Lookup
	}{
		{"no store wired at all", nil},
		{"store wired, account missing", func(context.Context, string) (sshutil.Config, error) {
			return sshutil.Config{}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withLookup(t, tc.lookup)
			res := run(t, core.Job{ID: "j", Params: map[string]any{
				"account": "web-1", "command": "uptime",
			}})
			if got := errCode(t, res); got != "not_connected" {
				t.Fatalf("error code = %q, want not_connected", got)
			}
			if !strings.Contains(res.Error.Message, "web-1") {
				t.Errorf("message does not name the account: %q", res.Error.Message)
			}
		})
	}
}

// The wired address is what makes one step serve a fleet, so it has to reach
// the dialler — with the chosen credential's login, not a different one.
func TestSSHRun_WiredHostOverridesTheCredential(t *testing.T) {
	withLookup(t, func(context.Context, string) (sshutil.Config, error) {
		return sshutil.Config{
			Host: "configured.example", Port: 22, Username: "deploy",
			Password: "p", Fingerprint: "SHA256:x",
		}, nil
	})
	res := run(t, core.Job{
		ID:     "j",
		Params: map[string]any{"account": "web-1", "command": "uptime"},
		// .invalid never resolves, so this fails in the dialler rather than
		// reaching a real machine — the error is where the address shows up.
		Input: map[string]core.Ref{"host": {MIME: "text/plain", Inline: "wired.invalid"}},
	})
	if got := errCode(t, res); got != "ssh_error" {
		t.Fatalf("error code = %q, want ssh_error", got)
	}
	if !strings.Contains(res.Error.Message, "wired.invalid") {
		t.Errorf("the wired address never reached the dialler: %q", res.Error.Message)
	}
	if strings.Contains(res.Error.Message, "configured.example") {
		t.Errorf("the credential's address was used instead: %q", res.Error.Message)
	}
}

func TestSSHRun_RejectsANonTextHostInput(t *testing.T) {
	withLookup(t, func(context.Context, string) (sshutil.Config, error) {
		return sshutil.Config{Host: "h", Port: 22, Username: "u", Password: "p"}, nil
	})
	res := run(t, core.Job{
		ID:     "j",
		Params: map[string]any{"account": "web-1", "command": "uptime"},
		Input:  map[string]core.Ref{"host": {MIME: "application/json", Inline: map[string]any{"not": "text"}}},
	})
	if got := errCode(t, res); got != "bad_input" {
		t.Errorf("error code = %q, want bad_input", got)
	}
}
