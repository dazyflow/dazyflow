// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// errUserStore lets each method fail on demand to drive ImportUsers'
// error branches.
type errUserStore struct {
	users   []User
	listErr error
	getErr  error
	putErr  error
}

func (s *errUserStore) GetByEmail(_ context.Context, _ string) (User, error) {
	if s.getErr != nil {
		return User{}, s.getErr
	}
	return User{}, ErrUnknownUser
}
func (s *errUserStore) PutUser(context.Context, User) error { return s.putErr }
func (s *errUserStore) ListUsers(context.Context) ([]User, error) {
	return s.users, s.listErr
}

func TestImportUsers_ErrorPaths(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")

	// Source list error.
	if _, _, err := ImportUsers(ctx, &errUserStore{listErr: boom}, &errUserStore{}); err == nil {
		t.Error("expected list error")
	}

	// Destination GetByEmail error (not ErrUnknownUser).
	src := &errUserStore{users: []User{{Email: "a@x.io"}}}
	if _, _, err := ImportUsers(ctx, src, &errUserStore{getErr: boom}); err == nil {
		t.Error("expected dst get error")
	}

	// Destination PutUser error.
	if _, _, err := ImportUsers(ctx, src, &errUserStore{putErr: boom}); err == nil {
		t.Error("expected dst put error")
	}
}

// erroringAuthenticator always fails, to drive ModerationGate's inner-error
// short-circuit.
type erroringAuthenticator struct{ err error }

func (e erroringAuthenticator) Authenticate(context.Context, string) (core.Principal, error) {
	return core.Principal{}, e.err
}

func TestModerationGate_InnerError(t *testing.T) {
	g := &ModerationGate{Inner: erroringAuthenticator{ErrInvalidCredential}}
	if _, err := g.Authenticate(context.Background(), "tok"); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("inner error not propagated: %v", err)
	}
}
