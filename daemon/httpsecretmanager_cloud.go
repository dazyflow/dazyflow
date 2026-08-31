// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"net/http"

	"github.com/dazyflow/dazyflow/core"
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
	getSecretManagerConfig(h, rw, r, p, "AWS secret-manager", awsConfigSecretName,
		func(cfg AwsSecretsConfig, configured bool) any {
			if !configured {
				return awsSecretManagerView{Configured: false}
			}
			return awsSecretManagerView{
				Configured:  true,
				Region:      cfg.Region,
				AccessKeyID: cfg.AccessKeyID,
				Endpoint:    cfg.Endpoint,
			}
		})
}

func (h *HTTPGateway) putSecretManagerAws(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	putSecretManagerConfig(h, rw, r, p, "AWS Secrets Manager", awsConfigSecretName,
		VerifyAwsConfig,
		// Audit the region + key id — never the secret access key.
		func(cfg AwsSecretsConfig) (string, string, string) {
			return "secret_manager.aws.put", cfg.Region, cfg.AccessKeyID
		})
}

func (h *HTTPGateway) deleteSecretManagerAws(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	deleteSecretManagerConfig(h, rw, r, p, "AWS Secrets Manager", awsConfigSecretName, "secret_manager.aws.delete")
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
	getSecretManagerConfig(h, rw, r, p, "GCP secret-manager", gcpConfigSecretName,
		func(cfg GcpSecretsConfig, configured bool) any {
			if !configured {
				return gcpSecretManagerView{Configured: false}
			}
			view := gcpSecretManagerView{Configured: true, ProjectID: cfg.ProjectID, Endpoint: cfg.Endpoint}
			if key, err := cfg.key(); err == nil {
				view.ClientEmail = key.ClientEmail
			}
			return view
		})
}

func (h *HTTPGateway) putSecretManagerGcp(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	putSecretManagerConfig(h, rw, r, p, "GCP Secret Manager", gcpConfigSecretName,
		VerifyGcpConfig,
		// Audit the project + service account — never the private key.
		func(cfg GcpSecretsConfig) (string, string, string) {
			email := ""
			if key, err := cfg.key(); err == nil {
				email = key.ClientEmail
			}
			return "secret_manager.gcp.put", cfg.ProjectID, email
		})
}

func (h *HTTPGateway) deleteSecretManagerGcp(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	deleteSecretManagerConfig(h, rw, r, p, "GCP Secret Manager", gcpConfigSecretName, "secret_manager.gcp.delete")
}
