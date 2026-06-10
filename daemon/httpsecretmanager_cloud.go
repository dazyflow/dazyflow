package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"git.sr.ht/~klahr/hazyflow/core"
)

// AWS / GCP variants of the BYO secret-manager config endpoints
// (httpsecretmanager.go holds the original Vault/OpenBao one):
//
//	GET    /api/v1/secret-manager/aws   → redacted view (no secret key)
//	PUT    /api/v1/secret-manager/aws   → set config; connection-tested
//	DELETE /api/v1/secret-manager/aws   → remove config
//	  …and the same trio under /secret-manager/gcp.
//
// Each provider has its own config slot, so a tenant can run vault://,
// aws://, and gcp:// references side by side.

// secretManagerGate centralizes the checks all six handlers share:
// encrypted store present, tenant-bound principal, and the right secret
// permission. Returns false after writing the error response.
func (h *HTTPGateway) secretManagerGate(rw http.ResponseWriter, p core.Principal, perm core.Permission) bool {
	if h.EncryptedSecrets == nil {
		writeJSONError(rw, http.StatusNotImplemented, "encrypted secret store is not configured")
		return false
	}
	if p.Tenant == "" {
		writeJSONError(rw, http.StatusForbidden, "principal has no tenant")
		return false
	}
	if err := core.Require(p, perm); err != nil {
		writeJSONError(rw, http.StatusForbidden, err.Error())
		return false
	}
	return true
}

// ── AWS ────────────────────────────────────────────────────────────────

// awsSecretManagerView is the credential-free GET shape: the key ID is an
// identifier (shown so the admin can tell WHICH key is wired), the secret
// access key never leaves the store.
type awsSecretManagerView struct {
	Configured  bool   `json:"configured"`
	Region      string `json:"region,omitempty"`
	AccessKeyID string `json:"access_key_id,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
}

func (h *HTTPGateway) getSecretManagerAws(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.secretManagerGate(rw, p, core.PermSecretRead) {
		return
	}
	cfg, ok, err := loadAwsConfig(r.Context(), h.EncryptedSecrets, p.Tenant)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("load AWS secret-manager config: %v", err))
		return
	}
	if !ok {
		writeJSON(rw, http.StatusOK, awsSecretManagerView{Configured: false})
		return
	}
	writeJSON(rw, http.StatusOK, awsSecretManagerView{
		Configured:  true,
		Region:      cfg.Region,
		AccessKeyID: cfg.AccessKeyID,
		Endpoint:    cfg.Endpoint,
	})
}

func (h *HTTPGateway) putSecretManagerAws(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.secretManagerGate(rw, p, core.PermSecretWrite) {
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxSecretValueBytes)
	var cfg AwsSecretsConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	if err := cfg.validate(); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), vaultVerifyTimeout)
	defer cancel()
	if err := VerifyAwsConfig(ctx, cfg, vaultVerifyTimeout); err != nil {
		writeJSONError(rw, http.StatusBadGateway, fmt.Sprintf("could not reach AWS Secrets Manager: %v", err))
		return
	}
	if err := saveAwsConfig(r.Context(), h.EncryptedSecrets, p.Tenant, cfg); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("save AWS secret-manager config: %v", err))
		return
	}
	// Audit the region + key id — never the secret access key.
	h.audit(r.Context(), p, "secret_manager.aws.put", cfg.Region, cfg.AccessKeyID)
	rw.WriteHeader(http.StatusNoContent)
}

func (h *HTTPGateway) deleteSecretManagerAws(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.secretManagerGate(rw, p, core.PermSecretWrite) {
		return
	}
	if err := deleteAwsConfig(r.Context(), h.EncryptedSecrets, p.Tenant); err != nil && !errors.Is(err, ErrSecretNotFound) {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("delete AWS secret-manager config: %v", err))
		return
	}
	h.audit(r.Context(), p, "secret_manager.aws.delete", "", "")
	rw.WriteHeader(http.StatusNoContent)
}

// ── GCP ────────────────────────────────────────────────────────────────

// gcpSecretManagerView is the credential-free GET shape: project + the
// service account's email (parsed from the stored key) identify the
// connection; the private key never leaves the store.
type gcpSecretManagerView struct {
	Configured  bool   `json:"configured"`
	ProjectID   string `json:"project_id,omitempty"`
	ClientEmail string `json:"client_email,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
}

func (h *HTTPGateway) getSecretManagerGcp(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.secretManagerGate(rw, p, core.PermSecretRead) {
		return
	}
	cfg, ok, err := loadGcpConfig(r.Context(), h.EncryptedSecrets, p.Tenant)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("load GCP secret-manager config: %v", err))
		return
	}
	if !ok {
		writeJSON(rw, http.StatusOK, gcpSecretManagerView{Configured: false})
		return
	}
	view := gcpSecretManagerView{Configured: true, ProjectID: cfg.ProjectID, Endpoint: cfg.Endpoint}
	if key, err := cfg.key(); err == nil {
		view.ClientEmail = key.ClientEmail
	}
	writeJSON(rw, http.StatusOK, view)
}

func (h *HTTPGateway) putSecretManagerGcp(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.secretManagerGate(rw, p, core.PermSecretWrite) {
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxSecretValueBytes)
	var cfg GcpSecretsConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	if err := cfg.validate(); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), vaultVerifyTimeout)
	defer cancel()
	if err := VerifyGcpConfig(ctx, cfg, vaultVerifyTimeout); err != nil {
		writeJSONError(rw, http.StatusBadGateway, fmt.Sprintf("could not reach GCP Secret Manager: %v", err))
		return
	}
	if err := saveGcpConfig(r.Context(), h.EncryptedSecrets, p.Tenant, cfg); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("save GCP secret-manager config: %v", err))
		return
	}
	// Audit the project + service account — never the private key.
	email := ""
	if key, err := cfg.key(); err == nil {
		email = key.ClientEmail
	}
	h.audit(r.Context(), p, "secret_manager.gcp.put", cfg.ProjectID, email)
	rw.WriteHeader(http.StatusNoContent)
}

func (h *HTTPGateway) deleteSecretManagerGcp(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.secretManagerGate(rw, p, core.PermSecretWrite) {
		return
	}
	if err := deleteGcpConfig(r.Context(), h.EncryptedSecrets, p.Tenant); err != nil && !errors.Is(err, ErrSecretNotFound) {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("delete GCP secret-manager config: %v", err))
		return
	}
	h.audit(r.Context(), p, "secret_manager.gcp.delete", "", "")
	rw.WriteHeader(http.StatusNoContent)
}
