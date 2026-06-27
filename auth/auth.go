// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package auth handles principal authentication. Two providers ship today:
// API keys (fully implemented and tested) and OIDC (scaffold — depends on
// coreos/go-oidc for production use; the spec wires Microsoft Entra, Okta,
// and Google Workspace as concrete IdPs).
package auth

import (
	"context"
	"errors"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Authenticator turns a bearer credential into a Principal. Implementations
// can be chained via Chain; the engine's HTTP/gRPC middleware picks the
// right Authenticator based on token prefix.
type Authenticator interface {
	// Authenticate verifies the credential (e.g. a JWT or API key) and
	// returns the resulting principal. Returns ErrInvalidCredential when
	// the input is malformed or unknown.
	Authenticate(ctx context.Context, credential string) (core.Principal, error)
}

var ErrInvalidCredential = errors.New("invalid credential")

// Chain tries each Authenticator in order and returns the first non-error
// principal. ErrInvalidCredential from individual providers is treated as
// "not my token" and falls through; other errors short-circuit.
type Chain []Authenticator

func (c Chain) Authenticate(ctx context.Context, credential string) (core.Principal, error) {
	if len(c) == 0 {
		return core.Principal{}, ErrInvalidCredential
	}
	var lastErr error = ErrInvalidCredential
	for _, a := range c {
		p, err := a.Authenticate(ctx, credential)
		if err == nil {
			return p, nil
		}
		if errors.Is(err, ErrInvalidCredential) {
			lastErr = err
			continue
		}
		return core.Principal{}, err
	}
	return core.Principal{}, lastErr
}

// BearerFromHeader extracts the credential portion of an "Authorization:
// Bearer <token>" header value. Returns ErrInvalidCredential if the header
// is missing or malformed.
func BearerFromHeader(header string) (string, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", ErrInvalidCredential
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", ErrInvalidCredential
	}
	return token, nil
}
