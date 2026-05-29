package daemon

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// IntegrationManifest is the daemon-side, loaded form of an integration's
// authoring manifest (see engine/jsdrop/sdk/hazyflow-integration.d.ts). An
// integration is an installable prerequisite — a provider / connection-type
// (e.g. "google") that drops depend on via requiresConnections. It is pure
// data (no run()): a provider recipe plus an install-time setup form, loaded
// with a plain unmarshal — no JS runtime, unlike drops.
//
// It carries NO credentials. The recipe (authorize/token URLs, scopes, flow)
// is public and signed; the operator's client_id/client_secret are supplied at
// install via the admin GUI (rendered from Setup) and stored encrypted.
type IntegrationManifest struct {
	ID          string          `json:"id"`
	Version     string          `json:"version"`
	Label       string          `json:"label"`
	Summary     string          `json:"summary"`
	Description string          `json:"description,omitempty"`
	Icon        string          `json:"icon,omitempty"`
	BrandLogo   string          `json:"brandLogo,omitempty"`
	DocsURL     string          `json:"docsUrl,omitempty"`
	Auth        IntegrationAuth `json:"auth"`
	Setup       []SetupField    `json:"setup,omitempty"`
}

// IntegrationAuth is the auth recipe. Kind discriminates which fields apply:
// "oauth2" uses the OAuth fields below; "secret" declares its credential
// field(s) in Setup; "none" needs no auth.
type IntegrationAuth struct {
	Kind            string            `json:"kind"`
	AuthorizeURL    string            `json:"authorizeUrl,omitempty"`
	TokenURL        string            `json:"tokenUrl,omitempty"`
	Scopes          []string          `json:"scopes,omitempty"`
	UsePKCE         *bool             `json:"usePKCE,omitempty"`
	Refreshable     *bool             `json:"refreshable,omitempty"`
	ClientAuth      string            `json:"clientAuth,omitempty"`
	AuthorizeParams map[string]string `json:"authorizeParams,omitempty"`
}

// SetupField is one input/display field the install GUI renders. "text" and
// "secret" collect operator input; "display" shows a read-only, host-templated
// value (e.g. the redirect URI). "secret" values are stored encrypted.
type SetupField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Help     string `json:"help,omitempty"`
	Value    string `json:"value,omitempty"`
}

// LoadIntegrationManifest parses and validates an integration manifest — the
// JSON the authored .ts reduces to.
func LoadIntegrationManifest(data []byte) (IntegrationManifest, error) {
	var m IntegrationManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return IntegrationManifest{}, fmt.Errorf("integration manifest: %w", err)
	}
	if m.ID == "" {
		return IntegrationManifest{}, fmt.Errorf("integration manifest: id is required")
	}
	if m.Version == "" {
		return IntegrationManifest{}, fmt.Errorf("integration %q: version is required", m.ID)
	}
	if m.Summary == "" {
		return IntegrationManifest{}, fmt.Errorf("integration %q: summary is required", m.ID)
	}
	switch m.Auth.Kind {
	case "oauth2", "secret", "none":
	default:
		return IntegrationManifest{}, fmt.Errorf("integration %q: unknown auth.kind %q", m.ID, m.Auth.Kind)
	}
	return m, nil
}

// toProvider maps an oauth2 integration manifest + operator credentials onto an
// OAuthProvider. (UsePKCE/Refreshable/ClientAuth have no field on OAuthProvider
// yet — the current flow's behavior is fixed; threading those toggles through
// is a follow-up for a provider that needs non-default behavior.)
func (m IntegrationManifest) toProvider(clientID, clientSecret string) (OAuthProvider, error) {
	if m.Auth.Kind != "oauth2" {
		return OAuthProvider{}, fmt.Errorf("integration %q: auth.kind is %q, not oauth2", m.ID, m.Auth.Kind)
	}
	if m.Auth.AuthorizeURL == "" || m.Auth.TokenURL == "" {
		return OAuthProvider{}, fmt.Errorf("integration %q: oauth2 requires authorizeUrl and tokenUrl", m.ID)
	}
	// The token URL receives the real client_secret in a back-channel POST and
	// the authorize URL receives the user's browser; a non-https endpoint (or an
	// http:// downgrade smuggled in via a manifest) would leak the secret or the
	// auth code. Require https for both.
	if err := requireHTTPS(m.ID, "authorizeUrl", m.Auth.AuthorizeURL); err != nil {
		return OAuthProvider{}, err
	}
	if err := requireHTTPS(m.ID, "tokenUrl", m.Auth.TokenURL); err != nil {
		return OAuthProvider{}, err
	}
	if clientID == "" || clientSecret == "" {
		return OAuthProvider{}, fmt.Errorf("integration %q: client_id and client_secret are required to register", m.ID)
	}
	return OAuthProvider{
		Name:            m.ID,
		AuthorizeURL:    m.Auth.AuthorizeURL,
		TokenURL:        m.Auth.TokenURL,
		Scopes:          m.Auth.Scopes,
		AuthorizeExtras: m.Auth.AuthorizeParams,
		ClientID:        clientID,
		ClientSecret:    clientSecret,
	}, nil
}

// RegisterFromManifest registers an OAuth provider from an installed
// integration manifest plus operator-supplied credentials. This is the
// data-driven registration path that replaces hardcoded provider tables for
// installed integrations — the entry point the install pipeline calls once an
// operator has configured an integration through the admin GUI.
func RegisterFromManifest(r *OAuthRegistry, m IntegrationManifest, clientID, clientSecret string) error {
	p, err := m.toProvider(clientID, clientSecret)
	if err != nil {
		return err
	}
	r.Register(p)
	return nil
}

// requireHTTPS rejects a manifest URL field that isn't a well-formed https URL.
func requireHTTPS(integrationID, field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("integration %q: %s is not a valid URL: %w", integrationID, field, err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("integration %q: %s must be an https URL, got %q", integrationID, field, raw)
	}
	return nil
}
