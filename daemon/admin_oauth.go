package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// Admin OAuth provider configuration — paste-client-credentials-in-
// the-UI for operators who'd otherwise have to set env vars and
// restart the daemon. Endpoints:
//
//	GET    /api/v1/admin/oauth-providers           — list known + their state
//	PUT    /api/v1/admin/oauth-providers/{name}    — set credentials, live-register
//	DELETE /api/v1/admin/oauth-providers/{name}    — clear credentials, unregister
//
// All gated on tenant:admin OR platform:admin (same pattern as the
// rest of /api/v1/admin/*). The endpoints write through the
// encrypted secret store and update the in-memory registry; no
// daemon restart needed for changes to take effect.

// adminProviderRow is the shape returned per provider in the list
// response. Combines the deployment-invariant metadata (display
// name, required scopes, setup help) with the runtime state
// (configured? when last updated? env-supplied?).
type adminProviderRow struct {
	Name         string    `json:"name"`
	DisplayName  string    `json:"display_name"`
	AuthorizeURL string    `json:"authorize_url"`
	Scopes       []string  `json:"scopes"`
	SetupHelp    string    `json:"setup_help"`
	RedirectURI  string    `json:"redirect_uri"`
	Configured   bool      `json:"configured"`
	HasPersisted bool      `json:"has_persisted"`
	HasEnv       bool      `json:"has_env"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}

// adminUpsertProviderRequest is the wire shape of PUT — only the two
// fields the operator pastes in. Scopes and URLs stay deployment-
// invariant (they're in KnownOAuthProviderDefaults).
type adminUpsertProviderRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func (h *HTTPGateway) listAdminOAuthProviders(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.OAuth == nil {
		writeJSONError(rw, http.StatusNotImplemented, "OAuth registry not configured")
		return
	}
	rows := make([]adminProviderRow, 0, len(KnownOAuthProviderDefaults))
	for _, def := range KnownOAuthProviderDefaults {
		row := adminProviderRow{
			Name:         def.Name,
			DisplayName:  def.DisplayName,
			AuthorizeURL: def.AuthorizeURL,
			Scopes:       def.Scopes,
			SetupHelp:    def.SetupHelp,
			RedirectURI:  h.OAuth.redirectURI(def.Name),
		}
		if registered, ok := h.OAuth.Provider(def.Name); ok && registered.ClientID != "" {
			row.Configured = true
		}
		if c, err := loadProviderCreds(r.Context(), h.EncryptedSecrets, def.Name); err == nil && c != nil {
			row.HasPersisted = true
			row.UpdatedAt = c.UpdatedAt
		}
		// HasEnv is "configured but no persisted creds" — i.e. the live
		// values came from HAZYFLOW_OAUTH_<NAME>_CLIENT_ID env vars at
		// boot. Useful so the admin UI can say "currently from env;
		// saving here will override".
		row.HasEnv = row.Configured && !row.HasPersisted
		rows = append(rows, row)
	}
	writeJSON(rw, http.StatusOK, map[string]any{"providers": rows})
}

func (h *HTTPGateway) upsertAdminOAuthProvider(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.OAuth == nil {
		writeJSONError(rw, http.StatusNotImplemented, "OAuth registry not configured")
		return
	}
	if h.EncryptedSecrets == nil {
		writeJSONError(rw, http.StatusNotImplemented, "encrypted secret store not configured")
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	def := providerDefault(name)
	if def == nil {
		writeJSONError(rw, http.StatusNotFound,
			fmt.Sprintf("unknown OAuth provider %q (known: %s)", name, knownProviderNames()))
		return
	}
	var body adminUpsertProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}
	body.ClientID = strings.TrimSpace(body.ClientID)
	body.ClientSecret = strings.TrimSpace(body.ClientSecret)
	if body.ClientID == "" || body.ClientSecret == "" {
		writeJSONError(rw, http.StatusBadRequest,
			"client_id and client_secret are both required")
		return
	}
	creds := providerCreds{
		ClientID:     body.ClientID,
		ClientSecret: body.ClientSecret,
		UpdatedAt:    time.Now().UTC(),
	}
	if err := saveProviderCreds(r.Context(), h.EncryptedSecrets, name, creds); err != nil {
		writeJSONError(rw, http.StatusInternalServerError, "persist: "+err.Error())
		return
	}
	// Live-register so the change takes effect without a restart.
	h.OAuth.Register(def.toProvider(creds.ClientID, creds.ClientSecret))
	// Audit so an operator can trace who changed credentials when.
	// secret values are NOT logged.
	h.audit(r.Context(), p, "oauth.provider.upsert", name, fmt.Sprintf("client_id=%s", redactClientID(creds.ClientID)))
	writeJSON(rw, http.StatusOK, map[string]any{
		"name":       name,
		"configured": true,
		"updated_at": creds.UpdatedAt,
	})
}

func (h *HTTPGateway) deleteAdminOAuthProvider(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.OAuth == nil {
		writeJSONError(rw, http.StatusNotImplemented, "OAuth registry not configured")
		return
	}
	if h.EncryptedSecrets == nil {
		writeJSONError(rw, http.StatusNotImplemented, "encrypted secret store not configured")
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if providerDefault(name) == nil {
		writeJSONError(rw, http.StatusNotFound, fmt.Sprintf("unknown OAuth provider %q", name))
		return
	}
	// Best-effort delete: the persisted store may simply not have an
	// entry (provider was env-only) — in that case unregister from the
	// in-memory registry only.
	if err := deleteProviderCreds(r.Context(), h.EncryptedSecrets, name); err != nil &&
		!strings.Contains(err.Error(), "not found") {
		writeJSONError(rw, http.StatusInternalServerError, "persist: "+err.Error())
		return
	}
	h.OAuth.Unregister(name)
	h.audit(r.Context(), p, "oauth.provider.delete", name, "")
	rw.WriteHeader(http.StatusNoContent)
}

// knownProviderNames joins the known catalogue names for error
// messages — operators can see at a glance which slug they should
// have used.
func knownProviderNames() string {
	names := make([]string, len(KnownOAuthProviderDefaults))
	for i, d := range KnownOAuthProviderDefaults {
		names[i] = d.Name
	}
	return strings.Join(names, ", ")
}

// redactClientID keeps the first 6 chars + ellipsis. Client IDs aren't
// secrets in OAuth's threat model (they ride in the redirect URL), but
// the audit log is a low-value place to publish them in full.
func redactClientID(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[:6] + "…"
}
