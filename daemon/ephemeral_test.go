// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/auth"
)

// The point of the whole change: state minted while serving one request is
// redeemable while serving the next, even when a different replica serves it.
//
// Each subtest uses TWO independent instances sharing only the store — which
// is what a package-level map could never satisfy, and is exactly how each of
// these regressed into needing sticky sessions.
func TestEphemeralState_MintedOnOneReplicaRedeemedOnAnother(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	shared := auth.NewMemEphemeralStore()

	t.Run("google sign-in state", func(t *testing.T) {
		podA := &authAPI{Ephemeral: shared}
		podB := &authAPI{Ephemeral: shared}

		state, err := podA.mintGoogleState(ctx, "acme", "/dash", "acme.example.com", "bind-nonce", true)
		if err != nil {
			t.Fatalf("mint on pod A: %v", err)
		}
		got, ok := podB.consumeGoogleState(ctx, state)
		if !ok {
			t.Fatal("pod B could not consume state pod A minted")
		}
		if got.Tenant != "acme" || got.ReturnTo != "/dash" ||
			got.Host != "acme.example.com" || got.Binding != "bind-nonce" || !got.Test {
			t.Fatalf("state crossed replicas but lost content: %+v", got)
		}
		// Single-use across replicas too, or a stolen state is replayable.
		if _, ok := podA.consumeGoogleState(ctx, state); ok {
			t.Fatal("state was consumable twice across replicas")
		}
	})

	t.Run("sign-in handoff", func(t *testing.T) {
		podA := &authAPI{Ephemeral: shared}
		podB := &authAPI{Ephemeral: shared}

		exp := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
		code, err := podA.mintHandoff(ctx, "sess-token-123", exp)
		if err != nil {
			t.Fatalf("mint on pod A: %v", err)
		}
		entry, ok := podB.consumeHandoff(ctx, code)
		if !ok {
			t.Fatal("pod B could not consume the handoff pod A minted")
		}
		if entry.Token != "sess-token-123" {
			t.Fatalf("handoff token = %q", entry.Token)
		}
		if !entry.ExpiresAt.Equal(exp) {
			t.Fatalf("handoff expiry = %v, want %v", entry.ExpiresAt, exp)
		}
		if _, ok := podA.consumeHandoff(ctx, code); ok {
			t.Fatal("handoff was consumable twice across replicas")
		}
	})

	t.Run("connector oauth pending authorization", func(t *testing.T) {
		podA := newOAuthStateStore(10 * time.Minute)
		podB := newOAuthStateStore(10 * time.Minute)
		podA.setStore(shared)
		podB.setStore(shared)

		state, err := podA.mint(pendingOAuth{
			tenant: "acme", provider: "google", account: "work",
			returnTo: "/connections", binding: "bind-abc", host: "acme.dazyflow.app",
		})
		if err != nil {
			t.Fatalf("mint on pod A: %v", err)
		}
		got, ok := podB.consume(state)
		if !ok {
			t.Fatal("pod B could not consume the pending authorization pod A minted")
		}
		if got.tenant != "acme" || got.provider != "google" || got.account != "work" ||
			got.returnTo != "/connections" || got.binding != "bind-abc" ||
			// The origin host has to survive too, or a callback served by
			// another replica cannot send the user back where they started.
			got.host != "acme.dazyflow.app" {
			t.Fatalf("pending authorization crossed replicas but lost content: %+v", got)
		}
		if got.created.IsZero() {
			t.Fatal("pending authorization lost its created stamp")
		}
		if _, ok := podA.consume(state); ok {
			t.Fatal("oauth state was consumable twice across replicas")
		}
	})

	t.Run("totp challenge", func(t *testing.T) {
		podA := auth.NewEphemeralTOTPChallengeStore(shared)
		podB := auth.NewEphemeralTOTPChallengeStore(shared)

		c := auth.TOTPChallenge{
			Email:     "ada@example.com",
			ExpiresAt: time.Now().Add(5 * time.Minute),
			Tenant:    "acme",
			Workspace: "main",
		}
		if err := podA.Put(ctx, "chal", c); err != nil {
			t.Fatalf("put on pod A: %v", err)
		}
		got, err := podB.Get(ctx, "chal")
		if err != nil {
			t.Fatalf("pod B could not read the challenge pod A stored: %v", err)
		}
		if got.Email != "ada@example.com" || got.Tenant != "acme" {
			t.Fatalf("challenge crossed replicas but lost content: %+v", got)
		}
		// The brute-force cap must count across replicas — otherwise each pod
		// grants a fresh budget of guesses.
		if n, err := podA.IncrAttempts(ctx, "chal"); err != nil || n != 1 {
			t.Fatalf("pod A IncrAttempts = %d / %v", n, err)
		}
		if n, err := podB.IncrAttempts(ctx, "chal"); err != nil || n != 2 {
			t.Fatalf("pod B IncrAttempts = %d / %v — the guess budget resets per replica", n, err)
		}
	})
}

// A store that is not wired must refuse rather than panic in a sign-in handler.
func TestEphemeralState_NoStoreIsAnErrorNotAPanic(t *testing.T) {
	t.Parallel()
	api := &authAPI{}
	if _, err := api.mintHandoff(context.Background(), "tok", time.Now().Add(time.Hour)); err == nil {
		t.Fatal("minting without a store should fail")
	}
	if _, ok := api.consumeHandoff(context.Background(), "anything"); ok {
		t.Fatal("consuming without a store should not succeed")
	}
}
