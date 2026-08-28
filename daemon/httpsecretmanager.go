// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Per-tenant "bring your own secret manager" config endpoints. A tenant points
// the platform at its own OpenBao/Vault; flows then resolve ${vault.PATH#FIELD}
// against it. The config is stored encrypted in the tenant's own store and is
// never returned with its credentials.
//
//	GET    /api/v1/secret-manager → redacted view (no token/secret_id)
//	PUT    /api/v1/secret-manager → set config; connection-tested before saving
//	DELETE /api/v1/secret-manager → remove config
//
// The PUT body is a VaultConfig. Reads/writes are gated on the same secret
// permissions as the built-in store.

const vaultVerifyTimeout = 10 * time.Second

// secretManagerView is the credential-free shape returned by GET — enough for
// the UI to show "configured, pointing at X via Y auth" without ever handing
// back the token or secret_id.
type secretManagerView struct {
	Configured bool   `json:"configured"`
	Address    string `json:"address,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Mount      string `json:"mount,omitempty"`
	AuthMethod string `json:"auth_method,omitempty"`
}

func (h *HTTPGateway) getSecretManager(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	getSecretManagerConfig(h, rw, r, p, "secret-manager", vaultConfigSecretName,
		func(cfg VaultConfig, configured bool) any {
			if !configured {
				return secretManagerView{Configured: false}
			}
			return secretManagerView{
				Configured: true,
				Address:    cfg.Address,
				Namespace:  cfg.Namespace,
				Mount:      cfg.Mount,
				AuthMethod: cfg.Auth.Method,
			}
		})
}

func (h *HTTPGateway) putSecretManager(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	putSecretManagerConfig(h, rw, r, p, "the secret manager", vaultConfigSecretName,
		VerifyVaultConfig,
		// Audit the connection target + auth method — never the credentials.
		func(cfg VaultConfig) (string, string, string) {
			return "secret_manager.put", cfg.Address, cfg.Auth.Method
		})
}

func (h *HTTPGateway) deleteSecretManager(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	deleteSecretManagerConfig(h, rw, r, p, "secret-manager", vaultConfigSecretName, "secret_manager.delete")
}
