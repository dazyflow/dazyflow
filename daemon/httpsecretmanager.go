package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
)

// Per-tenant "bring your own secret manager" config endpoints. A tenant points
// the platform at its own OpenBao/Vault; flows then resolve ${vault:PATH#FIELD}
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
	if h.EncryptedSecrets == nil {
		writeJSONError(rw, http.StatusNotImplemented, "encrypted secret store is not configured")
		return
	}
	if p.Tenant == "" {
		writeJSONError(rw, http.StatusForbidden, "principal has no tenant")
		return
	}
	if err := core.Require(p, core.PermSecretRead); err != nil {
		writeJSONError(rw, http.StatusForbidden, err.Error())
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
	if h.EncryptedSecrets == nil {
		writeJSONError(rw, http.StatusNotImplemented, "encrypted secret store is not configured")
		return
	}
	if p.Tenant == "" {
		writeJSONError(rw, http.StatusForbidden, "principal has no tenant")
		return
	}
	if err := core.Require(p, core.PermSecretWrite); err != nil {
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxSecretValueBytes)
	var cfg VaultConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	if err := cfg.validate(); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	// Connection-test before persisting, so a bad address/credential fails here
	// rather than silently breaking every flow that references a vault secret.
	ctx, cancel := context.WithTimeout(r.Context(), vaultVerifyTimeout)
	defer cancel()
	if err := VerifyVaultConfig(ctx, cfg, vaultVerifyTimeout); err != nil {
		writeJSONError(rw, http.StatusBadGateway, fmt.Sprintf("could not reach the secret manager: %v", err))
		return
	}
	if err := saveVaultConfig(r.Context(), h.EncryptedSecrets, p.Tenant, cfg); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("save secret-manager config: %v", err))
		return
	}
	// Audit the connection target + auth method — never the credentials.
	h.audit(r.Context(), p, "secret_manager.put", cfg.Address, cfg.Auth.Method)
	rw.WriteHeader(http.StatusNoContent)
}

func (h *HTTPGateway) deleteSecretManager(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.EncryptedSecrets == nil {
		writeJSONError(rw, http.StatusNotImplemented, "encrypted secret store is not configured")
		return
	}
	if p.Tenant == "" {
		writeJSONError(rw, http.StatusForbidden, "principal has no tenant")
		return
	}
	if err := core.Require(p, core.PermSecretWrite); err != nil {
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return
	}
	if err := deleteVaultConfig(r.Context(), h.EncryptedSecrets, p.Tenant); err != nil && !errors.Is(err, ErrSecretNotFound) {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("delete secret-manager config: %v", err))
		return
	}
	h.audit(r.Context(), p, "secret_manager.delete", "", "")
	rw.WriteHeader(http.StatusNoContent)
}
