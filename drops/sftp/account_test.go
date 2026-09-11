// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sftp

import (
	"context"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/sshcreds"
	"github.com/dazyflow/dazyflow/internal/sshutil"
)

func withLookup(t *testing.T, fn sshcreds.Lookup) {
	t.Helper()
	sshcreds.SetLookup(fn)
	t.Cleanup(func() { sshcreds.SetLookup(nil) })
}

// The whole point of keeping the old connection: a flow saved before named
// servers existed names no account, and must reach exactly the server it
// always did — even on a daemon that now has named servers too.
func TestSFTPConfig_NoAccountUsesTheOldConnection(t *testing.T) {
	withLookup(t, func(context.Context, string) (sshutil.Config, error) {
		return sshutil.Config{Host: "named.example", Username: "named", Password: "p"}, nil
	})
	cfg, err := sftpConfig(t.Context(), core.Job{ID: "j", Params: map[string]any{
		"host": "connection.example", "username": "conn", "password": "p", "directory": "/old",
	}})
	if err != nil {
		t.Fatalf("sftpConfig: %v", err)
	}
	if cfg.Host != "connection.example" || cfg.Username != "conn" {
		t.Errorf("a step with no account reached the wrong server: %+v", cfg)
	}
	if cfg.Directory != "/old" {
		t.Errorf("directory = %q, want /old", cfg.Directory)
	}
}

func TestSFTPConfig_AccountUsesTheNamedServer(t *testing.T) {
	withLookup(t, func(_ context.Context, account string) (sshutil.Config, error) {
		if account != "bank" {
			t.Errorf("looked up %q, want bank", account)
		}
		return sshutil.Config{
			Host: "sftp.bank.example", Username: "acme", Password: "p", Directory: "/incoming",
		}, nil
	})
	cfg, err := sftpConfig(t.Context(), core.Job{ID: "j", Params: map[string]any{"account": "bank"}})
	if err != nil {
		t.Fatalf("sftpConfig: %v", err)
	}
	if cfg.Host != "sftp.bank.example" || cfg.Username != "acme" {
		t.Errorf("named server not used: %+v", cfg)
	}
	if cfg.Directory != "/incoming" {
		t.Errorf("directory = %q, want the server's own /incoming", cfg.Directory)
	}
}

// The step's own folder is the per-transfer override; the server's is the
// default behind it; the account's home directory is the floor under both.
func TestSFTPConfig_FolderPrecedence(t *testing.T) {
	withLookup(t, func(context.Context, string) (sshutil.Config, error) {
		return sshutil.Config{Host: "h", Username: "u", Password: "p", Directory: "/server"}, nil
	})
	cfg, err := sftpConfig(t.Context(), core.Job{ID: "j", Params: map[string]any{
		"account": "bank", "directory": "/step",
	}})
	if err != nil {
		t.Fatalf("sftpConfig: %v", err)
	}
	if cfg.Directory != "/step" {
		t.Errorf("directory = %q, want the step's /step", cfg.Directory)
	}

	withLookup(t, func(context.Context, string) (sshutil.Config, error) {
		return sshutil.Config{Host: "h", Username: "u", Password: "p"}, nil
	})
	cfg, err = sftpConfig(t.Context(), core.Job{ID: "j", Params: map[string]any{"account": "bank"}})
	if err != nil {
		t.Fatalf("sftpConfig: %v", err)
	}
	if cfg.Directory != "." {
		t.Errorf("directory = %q, want the home directory %q", cfg.Directory, ".")
	}
}

func TestSFTPConfig_UnknownAccountNamesItself(t *testing.T) {
	withLookup(t, func(context.Context, string) (sshutil.Config, error) {
		return sshutil.Config{}, nil // configured nowhere
	})
	_, err := sftpConfig(t.Context(), core.Job{ID: "j", Params: map[string]any{"account": "supplier"}})
	if err == nil {
		t.Fatal("an unknown account was accepted")
	}
	if !strings.Contains(err.Error(), "supplier") {
		t.Errorf("error does not name the account: %v", err)
	}
}

// A named server with no username cannot be dialled, and the failure should
// name the credential rather than surface as an auth rejection later.
func TestSFTPConfig_IncompleteNamedServerIsRejected(t *testing.T) {
	withLookup(t, func(context.Context, string) (sshutil.Config, error) {
		return sshutil.Config{Host: "h"}, nil
	})
	_, err := sftpConfig(t.Context(), core.Job{ID: "j", Params: map[string]any{"account": "half"}})
	if err == nil || !strings.Contains(err.Error(), "username") {
		t.Errorf("error = %v, want one about the missing username", err)
	}
}

// With no connection AND no account there is nothing to dial, and the message
// has to offer both ways out.
func TestSFTPConfig_NothingConfiguredAtAll(t *testing.T) {
	withLookup(t, nil)
	_, err := sftpConfig(t.Context(), core.Job{ID: "j", Params: map[string]any{}})
	if err == nil {
		t.Fatal("a step with no server at all was accepted")
	}
	if !strings.Contains(err.Error(), "saved server") {
		t.Errorf("error does not mention picking a saved server: %v", err)
	}
}
