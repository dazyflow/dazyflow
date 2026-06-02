package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
)

// Marketplace install endpoints (platform-admin only). They mirror the admin
// OAuth API: gate on requirePlatformAdmin, read JSON, write JSON. The Installer
// holds the orchestration + persistence; these handlers are the thin HTTP
// surface the admin GUI drives.

type installIntegrationRequest struct {
	// Manifest is the integration manifest (the JSON the authored .ts reduces
	// to). In the fuller flow this is fetched from the integration repo; until
	// the transport layer exists the operator/GUI supplies it directly.
	Manifest json.RawMessage `json:"manifest"`
	// Credentials are the values collected from the manifest's setup form,
	// e.g. {"client_id": "...", "client_secret": "..."}.
	Credentials map[string]string `json:"credentials,omitempty"`
	// Signature is the optional detached signature over the manifest bytes; it
	// determines the trust tier. Omitted → community.
	Signature *Signature `json:"signature,omitempty"`
}

type installDropRequest struct {
	Name      string     `json:"name"`
	Source    string     `json:"source"`
	Signature *Signature `json:"signature,omitempty"`
	// Acknowledged must be true to install: the admin has seen the drop's
	// requested capabilities (preview) and consents. A runtime-installed drop
	// runs untrusted code, so the install refuses without explicit consent.
	Acknowledged bool `json:"acknowledged,omitempty"`
}

// integrationPreview is the "render the setup form" payload: the validated
// manifest header plus the setup fields with display values templated for this
// deployment (e.g. the redirect URI filled in), and the trust tier the
// supplied signature (if any) resolves to.
type integrationPreview struct {
	ID       string       `json:"id"`
	Version  string       `json:"version"`
	Label    string       `json:"label"`
	Summary  string       `json:"summary"`
	AuthKind string       `json:"auth_kind"`
	Scopes   []string     `json:"scopes,omitempty"`
	Setup    []SetupField `json:"setup"`
	Tier     TrustTier    `json:"tier"`
	// Commit is the resolved git commit (the immutable digest) when previewed
	// from a repo; empty for a directly-supplied manifest.
	Commit string `json:"commit,omitempty"`
}

type gitRef struct {
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
}

type gitInstallIntegrationRequest struct {
	gitRef
	Credentials map[string]string `json:"credentials,omitempty"`
}

type gitInstallDropRequest struct {
	gitRef
	Path         string `json:"path"`
	Acknowledged bool   `json:"acknowledged,omitempty"`
}

// dropCapabilitySummary is the consent payload shown before installing a drop:
// the exact access the drop's manifest declares, so a platform admin grants it
// knowingly. A runtime-installed drop always runs sandboxed (out-of-process,
// resource-limited, broker-mediated); the residual exfiltration surface is the
// secrets/OAuth it can read and the egress hosts it can reach — those are what
// the admin is really trusting, so they lead the summary.
type dropCapabilitySummary struct {
	ID        string    `json:"id"`
	Version   string    `json:"version"`
	Label     string    `json:"label"`
	Summary   string    `json:"summary"`
	Tier      TrustTier `json:"tier"`
	Sandboxed bool      `json:"sandboxed"`        // always true for an installed drop
	OAuth     []string  `json:"oauth"`            // OAuth providers it requests tokens for
	Secrets   []string  `json:"secrets"`          // named secrets it reads
	Egress    []string  `json:"egress"`           // declared outbound hosts ([] = no network)
	Commit    string    `json:"commit,omitempty"` // resolved git commit when previewed from a repo
}

// dropCapabilities extracts the consent summary from a compiled manifest.
func dropCapabilities(m core.Manifest, tier TrustTier, commit string) dropCapabilitySummary {
	s := dropCapabilitySummary{
		ID: m.ID, Version: m.Version, Label: m.Label, Summary: m.Summary,
		Tier: tier, Sandboxed: true, Commit: commit,
		OAuth: []string{}, Secrets: []string{}, Egress: []string{},
	}
	for _, rc := range m.RequiresConnections {
		switch rc.Kind {
		case "oauth":
			s.OAuth = append(s.OAuth, rc.Name)
		case "secret":
			s.Secrets = append(s.Secrets, rc.Name)
		}
	}
	s.Egress = append(s.Egress, m.Egress...)
	return s
}

// previewIntegration validates a manifest and returns the setup form to render.
// It does NOT install — it's the GET-the-form step before POSTing credentials.
func (h *HTTPGateway) previewIntegration(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.Installer == nil {
		writeJSONError(rw, http.StatusNotImplemented, "marketplace install is not configured")
		return
	}
	var body installIntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}
	m, err := LoadIntegrationManifest(body.Manifest)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	// Resolve the trust tier the signature (if any) yields, so the GUI can show
	// the badge on the install form. A forged signature is surfaced as an error.
	tier, _, verr := h.Installer.Verify(body.Manifest, body.Signature)
	if verr != nil {
		writeJSONError(rw, http.StatusBadRequest, verr.Error())
		return
	}
	writeJSON(rw, http.StatusOK, integrationPreview{
		ID:       m.ID,
		Version:  m.Version,
		Label:    m.Label,
		Summary:  m.Summary,
		AuthKind: m.Auth.Kind,
		Scopes:   m.Auth.Scopes,
		Setup:    h.resolveSetup(m),
		Tier:     tier,
	})
}

// installIntegration installs an integration: registers its provider (oauth2)
// from the manifest + supplied credentials and persists it.
func (h *HTTPGateway) installIntegration(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.Installer == nil {
		writeJSONError(rw, http.StatusNotImplemented, "marketplace install is not configured")
		return
	}
	var body installIntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}
	m, tier, err := h.Installer.InstallIntegration(r.Context(), body.Manifest, body.Credentials, body.Signature, Provenance{})
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r.Context(), p, "marketplace.integration.install", m.ID, fmt.Sprintf("version=%s tier=%s", m.Version, tier))
	writeJSON(rw, http.StatusOK, map[string]any{"id": m.ID, "version": m.Version, "tier": tier, "installed": true})
}

// listInstalledIntegrations returns the integrations the platform supports.
func (h *HTTPGateway) listInstalledIntegrations(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.Installer == nil {
		writeJSONError(rw, http.StatusNotImplemented, "marketplace install is not configured")
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"integrations": h.Installer.InstalledIntegrations()})
}

// previewDrop compiles a drop source and returns its requested capabilities for
// the install consent screen. It does NOT install.
func (h *HTTPGateway) previewDrop(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.Installer == nil {
		writeJSONError(rw, http.StatusNotImplemented, "marketplace install is not configured")
		return
	}
	var body installDropRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Source) == "" {
		writeJSONError(rw, http.StatusBadRequest, "source is required")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "drop"
	}
	man, err := h.Installer.InspectDrop(name, body.Source)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	tier, _, verr := h.Installer.Verify([]byte(body.Source), body.Signature)
	if verr != nil {
		writeJSONError(rw, http.StatusBadRequest, verr.Error())
		return
	}
	writeJSON(rw, http.StatusOK, dropCapabilities(man, tier, ""))
}

// previewDropFromGit fetches a drop from repo@ref:path and returns its requested
// capabilities for the consent screen (with the resolved commit). No install.
func (h *HTTPGateway) previewDropFromGit(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.Installer == nil {
		writeJSONError(rw, http.StatusNotImplemented, "marketplace install is not configured")
		return
	}
	var body gitInstallDropRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Path) == "" {
		writeJSONError(rw, http.StatusBadRequest, "path is required")
		return
	}
	fetched, err := (GitSource{}).Fetch(r.Context(), body.Repo, body.Ref)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	source, err := fetched.File(body.Path)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	sig, err := fetched.Signature(body.Path)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	man, err := h.Installer.InspectDrop(body.Path, string(source))
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	tier, _, verr := h.Installer.Verify(source, sig)
	if verr != nil {
		writeJSONError(rw, http.StatusBadRequest, verr.Error())
		return
	}
	writeJSON(rw, http.StatusOK, dropCapabilities(man, tier, fetched.Commit))
}

// installDrop installs a drop, gated on its required integrations and on
// explicit capability consent. A missing prerequisite is a 409 (install the
// integration first); missing consent or other validation failures are 400.
func (h *HTTPGateway) installDrop(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.Installer == nil {
		writeJSONError(rw, http.StatusNotImplemented, "marketplace install is not configured")
		return
	}
	var body installDropRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Source) == "" {
		writeJSONError(rw, http.StatusBadRequest, "source is required")
		return
	}
	if !body.Acknowledged {
		writeJSONError(rw, http.StatusBadRequest, "capability consent required: preview the drop and acknowledge its requested access before installing")
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "drop"
	}
	man, tier, err := h.Installer.InstallDrop(r.Context(), name, body.Source, body.Signature, Provenance{})
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "is not installed") {
			status = http.StatusConflict // unmet integration prerequisite
		}
		writeJSONError(rw, status, err.Error())
		return
	}
	h.audit(r.Context(), p, "marketplace.drop.install", man.ID, "tier="+string(tier))
	writeJSON(rw, http.StatusOK, map[string]any{"id": man.ID, "tier": tier, "installed": true})
}

// listInstalledDrops returns the installed drops' manifests.
func (h *HTTPGateway) listInstalledDrops(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.Installer == nil {
		writeJSONError(rw, http.StatusNotImplemented, "marketplace install is not configured")
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"drops": h.Installer.InstalledDrops()})
}

// previewIntegrationFromGit fetches a repo@ref, reads integration.json (+ its
// optional .sig), and returns the setup form + trust tier + resolved commit —
// the GUI's "paste a repo, see what you're about to install" step.
func (h *HTTPGateway) previewIntegrationFromGit(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.Installer == nil {
		writeJSONError(rw, http.StatusNotImplemented, "marketplace install is not configured")
		return
	}
	var body gitRef
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}
	fetched, err := (GitSource{}).Fetch(r.Context(), body.Repo, body.Ref)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	manifestBytes, err := fetched.File(integrationManifestFile)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	sig, err := fetched.Signature(integrationManifestFile)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	m, err := LoadIntegrationManifest(manifestBytes)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	tier, _, verr := h.Installer.Verify(manifestBytes, sig)
	if verr != nil {
		writeJSONError(rw, http.StatusBadRequest, verr.Error())
		return
	}
	writeJSON(rw, http.StatusOK, integrationPreview{
		ID:       m.ID,
		Version:  m.Version,
		Label:    m.Label,
		Summary:  m.Summary,
		AuthKind: m.Auth.Kind,
		Scopes:   m.Auth.Scopes,
		Setup:    h.resolveSetup(m),
		Tier:     tier,
		Commit:   fetched.Commit,
	})
}

// installIntegrationFromGit fetches + installs an integration from repo@ref
// with operator-supplied credentials.
func (h *HTTPGateway) installIntegrationFromGit(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.Installer == nil {
		writeJSONError(rw, http.StatusNotImplemented, "marketplace install is not configured")
		return
	}
	var body gitInstallIntegrationRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}
	m, tier, err := h.Installer.InstallIntegrationFromGit(r.Context(), body.Repo, body.Ref, body.Credentials)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r.Context(), p, "marketplace.integration.install_from_git", m.ID, fmt.Sprintf("%s@%s tier=%s", body.Repo, body.Ref, tier))
	writeJSON(rw, http.StatusOK, map[string]any{"id": m.ID, "version": m.Version, "tier": tier, "installed": true})
}

// installDropFromGit fetches + installs a drop from repo@ref:path (gated on its
// integration prerequisites).
func (h *HTTPGateway) installDropFromGit(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.Installer == nil {
		writeJSONError(rw, http.StatusNotImplemented, "marketplace install is not configured")
		return
	}
	var body gitInstallDropRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Path) == "" {
		writeJSONError(rw, http.StatusBadRequest, "path is required")
		return
	}
	if !body.Acknowledged {
		writeJSONError(rw, http.StatusBadRequest, "capability consent required: preview the drop and acknowledge its requested access before installing")
		return
	}
	man, tier, err := h.Installer.InstallDropFromGit(r.Context(), body.Repo, body.Ref, body.Path)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "is not installed") {
			status = http.StatusConflict
		}
		writeJSONError(rw, status, err.Error())
		return
	}
	h.audit(r.Context(), p, "marketplace.drop.install_from_git", man.ID, fmt.Sprintf("%s@%s:%s tier=%s", body.Repo, body.Ref, body.Path, tier))
	writeJSON(rw, http.StatusOK, map[string]any{"id": man.ID, "tier": tier, "installed": true})
}

// uninstallIntegration removes an integration. A 409 means an installed drop
// still depends on it (uninstall the drops first).
func (h *HTTPGateway) uninstallIntegration(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.Installer == nil {
		writeJSONError(rw, http.StatusNotImplemented, "marketplace install is not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONError(rw, http.StatusBadRequest, "id is required")
		return
	}
	if err := h.Installer.UninstallIntegration(r.Context(), id); err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "is required by") {
			status = http.StatusConflict
		}
		writeJSONError(rw, status, err.Error())
		return
	}
	h.audit(r.Context(), p, "marketplace.integration.uninstall", id, "")
	rw.WriteHeader(http.StatusNoContent)
}

// uninstallDrop removes a drop.
func (h *HTTPGateway) uninstallDrop(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if err := requirePlatformAdmin(p); err != nil {
		adminError(rw, err)
		return
	}
	if h.Installer == nil {
		writeJSONError(rw, http.StatusNotImplemented, "marketplace install is not configured")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeJSONError(rw, http.StatusBadRequest, "id is required")
		return
	}
	if err := h.Installer.UninstallDrop(r.Context(), id); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r.Context(), p, "marketplace.drop.uninstall", id, "")
	rw.WriteHeader(http.StatusNoContent)
}

// resolveSetup returns the manifest's setup fields with display-type values
// templated for this deployment ({publicBaseUrl}, {id}) — so the GUI renders a
// ready-to-copy redirect URI without knowing the public base URL itself.
func (h *HTTPGateway) resolveSetup(m IntegrationManifest) []SetupField {
	base := ""
	if h.svc != nil {
		base = strings.TrimRight(h.svc.PublicBaseURL, "/")
	}
	repl := strings.NewReplacer("{publicBaseUrl}", base, "{id}", m.ID)
	out := make([]SetupField, len(m.Setup))
	for i, f := range m.Setup {
		if f.Type == "display" {
			f.Value = repl.Replace(f.Value)
		}
		out[i] = f
	}
	return out
}
