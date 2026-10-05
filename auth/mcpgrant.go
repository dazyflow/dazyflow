// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// An MCP grant is what a user approves when they connect an MCP client
// (claude.ai, the Claude apps, Claude Code) to dzd's /mcp over OAuth: one
// record per approved connection, carrying the principal it acts as and the
// hashes of its current access and refresh tokens.
//
// The principal is a snapshot taken at approval, exactly as a session's is at
// sign-in; suspension is still enforced live by ModerationGate. Tokens are
// stored as SHA-256 hashes only — see SessionLookupKey for why unsalted is
// enough for 256-bit random secrets.
const (
	MCPAccessTokenPrefix  = "dzo_"
	MCPRefreshTokenPrefix = "dzr_"
	MCPAccessTTL          = time.Hour
	MCPRefreshTTL         = 30 * 24 * time.Hour
)

// ErrMCPRefreshReused reports that a refresh token was presented after it had
// already been rotated: either the client replayed it or it leaked. The grant
// is deleted, per OAuth 2.1's refresh-token rotation guidance.
var ErrMCPRefreshReused = errors.New("refresh token reused; connection revoked")

type MCPGrant struct {
	ID        string
	Subject   string
	Tenant    string
	Workspace string
	Roles     []core.Role
	// ClientName is what the client registered as, and RedirectHost where it
	// sent the user back; both are shown on the connections list so the user
	// can tell their connections apart. Neither is trusted for anything.
	ClientName       string
	RedirectHost     string
	Scope            string
	AccessHash       string
	AccessExpiresAt  time.Time
	RefreshHash      string
	PrevRefreshHash  string
	RefreshExpiresAt time.Time
	CreatedAt        time.Time
	RefreshedAt      time.Time
}

type MCPGrantStore interface {
	PutGrant(ctx context.Context, g MCPGrant) error
	// GetGrant returns ErrInvalidCredential for an unknown id.
	GetGrant(ctx context.Context, id string) (MCPGrant, error)
	// SwapGrant replaces the stored grant only while its RefreshHash still
	// equals oldRefreshHash, so of two racing refreshes exactly one rotates;
	// the loser gets ErrInvalidCredential.
	SwapGrant(ctx context.Context, g MCPGrant, oldRefreshHash string) error
	DeleteGrant(ctx context.Context, id string) error
	ListGrantsBySubject(ctx context.Context, subject string) ([]MCPGrant, error)
	RevokeSubjectGrants(ctx context.Context, subject string) (int, error)
}

// IssueMCPGrant stores g (identity and display fields set by the caller) with
// a fresh id and token pair, returning the cleartext tokens.
func IssueMCPGrant(ctx context.Context, store MCPGrantStore, g MCPGrant, now time.Time) (MCPGrant, string, string, error) {
	id, err := randomHex(8)
	if err != nil {
		return MCPGrant{}, "", "", err
	}
	g.ID = "g" + id
	g.CreatedAt = now
	access, refresh, err := mintMCPTokens(&g, now)
	if err != nil {
		return MCPGrant{}, "", "", err
	}
	if err := store.PutGrant(ctx, g); err != nil {
		return MCPGrant{}, "", "", err
	}
	return g, access, refresh, nil
}

// RefreshMCPGrant rotates both tokens of the grant refreshToken belongs to.
func RefreshMCPGrant(ctx context.Context, store MCPGrantStore, refreshToken string, now time.Time) (MCPGrant, string, string, error) {
	id, ok := mcpTokenGrantID(refreshToken, MCPRefreshTokenPrefix)
	if !ok {
		return MCPGrant{}, "", "", ErrInvalidCredential
	}
	g, err := store.GetGrant(ctx, id)
	if err != nil {
		return MCPGrant{}, "", "", ErrInvalidCredential
	}
	presented := SessionLookupKey(refreshToken)
	if g.PrevRefreshHash != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(g.PrevRefreshHash)) == 1 {
		_ = store.DeleteGrant(ctx, id)
		return MCPGrant{}, "", "", ErrMCPRefreshReused
	}
	if subtle.ConstantTimeCompare([]byte(presented), []byte(g.RefreshHash)) != 1 {
		return MCPGrant{}, "", "", ErrInvalidCredential
	}
	if now.After(g.RefreshExpiresAt) {
		_ = store.DeleteGrant(ctx, id)
		return MCPGrant{}, "", "", fmt.Errorf("%w: refresh token expired", ErrInvalidCredential)
	}
	old := g.RefreshHash
	g.PrevRefreshHash = old
	g.RefreshedAt = now
	access, refresh, err := mintMCPTokens(&g, now)
	if err != nil {
		return MCPGrant{}, "", "", err
	}
	if err := store.SwapGrant(ctx, g, old); err != nil {
		return MCPGrant{}, "", "", err
	}
	return g, access, refresh, nil
}

func mintMCPTokens(g *MCPGrant, now time.Time) (access, refresh string, err error) {
	a, err := randomHex(32)
	if err != nil {
		return "", "", err
	}
	r, err := randomHex(32)
	if err != nil {
		return "", "", err
	}
	access = MCPAccessTokenPrefix + g.ID + "_" + a
	refresh = MCPRefreshTokenPrefix + g.ID + "_" + r
	g.AccessHash = SessionLookupKey(access)
	g.AccessExpiresAt = now.Add(MCPAccessTTL)
	g.RefreshHash = SessionLookupKey(refresh)
	g.RefreshExpiresAt = now.Add(MCPRefreshTTL)
	return access, refresh, nil
}

func mcpTokenGrantID(token, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(token, prefix)
	if !ok {
		return "", false
	}
	id, secret, ok := strings.Cut(rest, "_")
	return id, ok && id != "" && secret != ""
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

type MCPGrantAuthenticator struct {
	Store MCPGrantStore
	Clock func() time.Time
}

func (a *MCPGrantAuthenticator) Authenticate(ctx context.Context, credential string) (core.Principal, error) {
	id, ok := mcpTokenGrantID(credential, MCPAccessTokenPrefix)
	if !ok {
		return core.Principal{}, ErrInvalidCredential
	}
	g, err := a.Store.GetGrant(ctx, id)
	if err != nil {
		return core.Principal{}, ErrInvalidCredential
	}
	if subtle.ConstantTimeCompare([]byte(SessionLookupKey(credential)), []byte(g.AccessHash)) != 1 {
		return core.Principal{}, ErrInvalidCredential
	}
	if a.now().After(g.AccessExpiresAt) {
		return core.Principal{}, fmt.Errorf("%w: access token expired", ErrInvalidCredential)
	}
	return core.Principal{
		Subject:   g.Subject,
		Tenant:    g.Tenant,
		Workspace: g.Workspace,
		Roles:     core.UpgradeLegacyRoles(g.Roles),
	}, nil
}

func (a *MCPGrantAuthenticator) now() time.Time {
	if a.Clock != nil {
		return a.Clock()
	}
	return time.Now()
}

type MemMCPGrantStore struct {
	mu     sync.Mutex
	grants map[string]MCPGrant
}

func NewMemMCPGrantStore() *MemMCPGrantStore {
	return &MemMCPGrantStore{grants: map[string]MCPGrant{}}
}

func (s *MemMCPGrantStore) PutGrant(_ context.Context, g MCPGrant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants[g.ID] = g
	return nil
}

func (s *MemMCPGrantStore) GetGrant(_ context.Context, id string) (MCPGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.grants[id]
	if !ok {
		return MCPGrant{}, ErrInvalidCredential
	}
	return g, nil
}

func (s *MemMCPGrantStore) SwapGrant(_ context.Context, g MCPGrant, oldRefreshHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.grants[g.ID]
	if !ok || cur.RefreshHash != oldRefreshHash {
		return ErrInvalidCredential
	}
	s.grants[g.ID] = g
	return nil
}

func (s *MemMCPGrantStore) DeleteGrant(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.grants, id)
	return nil
}

func (s *MemMCPGrantStore) ListGrantsBySubject(_ context.Context, subject string) ([]MCPGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []MCPGrant
	for _, g := range s.grants {
		if g.Subject == subject {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *MemMCPGrantStore) RevokeSubjectGrants(_ context.Context, subject string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, g := range s.grants {
		if g.Subject == subject {
			delete(s.grants, id)
			n++
		}
	}
	return n, nil
}
