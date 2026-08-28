// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// fakeAuthenticator returns a fixed principal, simulating a valid
// credential the inner chain accepted.
type fakeAuthenticator struct{ p core.Principal }

func (f fakeAuthenticator) Authenticate(context.Context, string) (core.Principal, error) {
	return f.p, nil
}

// fakeUsers / fakeOrgs are minimal in-memory stores for the gate test.
type fakeUsers map[string]User

func (m fakeUsers) GetByEmail(_ context.Context, email string) (User, error) {
	u, ok := m[email]
	if !ok {
		return User{}, ErrUnknownUser
	}
	return u, nil
}
func (m fakeUsers) PutUser(context.Context, User) error       { return nil }
func (m fakeUsers) ListUsers(context.Context) ([]User, error) { return nil, nil }

type fakeOrgs map[string]OrgProfile

func (m fakeOrgs) GetOrgProfile(_ context.Context, tenant string) (OrgProfile, error) {
	p, ok := m[tenant]
	if !ok {
		return OrgProfile{}, ErrUnknownOrgProfile
	}
	return p, nil
}
func (m fakeOrgs) PutOrgProfile(context.Context, OrgProfile) error { return nil }
func (m fakeOrgs) ListOrgProfiles(context.Context, []string) (map[string]OrgProfile, error) {
	return nil, nil
}
func (m fakeOrgs) GetOrgProfileBySubdomain(context.Context, string) (OrgProfile, error) {
	return OrgProfile{}, ErrUnknownOrgProfile
}

func TestModerationGate(t *testing.T) {
	principal := core.Principal{Subject: "alice@acme.test", Tenant: "org_acme"}
	gate := func(users fakeUsers, orgs fakeOrgs) *ModerationGate {
		return &ModerationGate{Inner: fakeAuthenticator{principal}, Users: users, Orgs: orgs}
	}

	t.Run("active passes through", func(t *testing.T) {
		g := gate(
			fakeUsers{"alice@acme.test": {Email: "alice@acme.test", Status: StatusActive}},
			fakeOrgs{"org_acme": {Tenant: "org_acme", Status: StatusActive}},
		)
		got, err := g.Authenticate(context.Background(), "tok")
		if err != nil {
			t.Fatalf("active principal rejected: %v", err)
		}
		if got.Subject != "alice@acme.test" {
			t.Fatalf("subject = %q", got.Subject)
		}
	})

	t.Run("suspended user is refused", func(t *testing.T) {
		g := gate(
			fakeUsers{"alice@acme.test": {Email: "alice@acme.test", Status: StatusSuspended}},
			fakeOrgs{"org_acme": {Tenant: "org_acme", Status: StatusActive}},
		)
		_, err := g.Authenticate(context.Background(), "tok")
		if !errors.Is(err, ErrAccountSuspended) {
			t.Fatalf("want ErrAccountSuspended, got %v", err)
		}
	})

	t.Run("suspended org is refused", func(t *testing.T) {
		g := gate(
			fakeUsers{"alice@acme.test": {Email: "alice@acme.test", Status: StatusActive}},
			fakeOrgs{"org_acme": {Tenant: "org_acme", Status: StatusSuspended}},
		)
		_, err := g.Authenticate(context.Background(), "tok")
		if !errors.Is(err, ErrAccountSuspended) {
			t.Fatalf("want ErrAccountSuspended, got %v", err)
		}
	})

	t.Run("missing user record (service account) passes when org active", func(t *testing.T) {
		g := gate(
			fakeUsers{}, // subject isn't a real user — e.g. an API-key service account
			fakeOrgs{"org_acme": {Tenant: "org_acme", Status: StatusActive}},
		)
		if _, err := g.Authenticate(context.Background(), "tok"); err != nil {
			t.Fatalf("service-account principal rejected: %v", err)
		}
	})

	t.Run("nil stores skip the checks", func(t *testing.T) {
		g := &ModerationGate{Inner: fakeAuthenticator{principal}}
		if _, err := g.Authenticate(context.Background(), "tok"); err != nil {
			t.Fatalf("gate with nil stores rejected: %v", err)
		}
	})
}

func TestEmailDomain(t *testing.T) {
	cases := map[string]string{
		"alice@acme.test": "acme.test",
		"BOB@Acme.Test":   "acme.test",
		"nope":            "",
		"@no-local":       "", // empty local part — no usable address
		"trailing@":       "",
	}
	for in, want := range cases {
		if got := emailDomain(in); got != want {
			t.Errorf("emailDomain(%q) = %q, want %q", in, got, want)
		}
	}
}
