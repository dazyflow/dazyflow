package daemon

import (
	"fmt"
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
	if !h.secretManagerGate(rw, p, core.PermSecretRead) {
		return
	}
	cfg, ok, err := loadVaultConfig(r.Context(), h.EncryptedSecrets, p.Tenant)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("load secret-manager config: %v", err))
		return
	}
	if !ok {
		writeJSON(rw, http.StatusOK, secretManagerView{Configured: false})
		return
	}
	writeJSON(rw, http.StatusOK, secretManagerView{
		Configured: true,
		Address:    cfg.Address,
		Namespace:  cfg.Namespace,
		Mount:      cfg.Mount,
		AuthMethod: cfg.Auth.Method,
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
