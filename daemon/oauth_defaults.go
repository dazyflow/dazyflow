package daemon

// OAuthProviderDefault is the URLs+scopes side of an OAuthProvider —
// everything except the per-deployment ClientID/ClientSecret. The
// defaults table below is the canonical "what services can a Hazy
// Flow install talk to OAuth-wise", used both at boot (env-var
// hydration in cmd/hzd) and at admin runtime (so the UI can list
// every connectable service even before its credentials are pasted
// in).
//
// Adding a new connector means adding one entry here. The URLs +
// scopes are deployment-invariant; clients change per install and
// come from env or the admin endpoint.
type OAuthProviderDefault struct {
	Name            string
	AuthorizeURL    string
	TokenURL        string
	Scopes          []string
	AuthorizeExtras map[string]string
	// DisplayName is the user-friendly label shown in the admin UI
	// next to the configuration row. Avoids hard-coding the
	// capitalisation map in the frontend.
	DisplayName string
	// SetupHelp is one-line operator guidance — "where do I get this
	// client ID?" — rendered under the paste boxes in the admin UI.
	SetupHelp string
}

// KnownOAuthProviderDefaults is the deployment-invariant catalogue of
// OAuth providers Hazy Flow's drops can use. Order matters: it's the
// order admin UI rows render in.
//
// drive.readonly on the Google entry is required for sheets_export_pdf
// (Drive's files.export endpoint). Existing connected accounts that
// predate this scope show a "Reconnect required" pill on the user-
// facing Connections page (scope_mismatch check) until they
// reauthorize.
var KnownOAuthProviderDefaults = []OAuthProviderDefault{
	{
		Name:         "slack",
		DisplayName:  "Slack",
		AuthorizeURL: "https://slack.com/oauth/v2/authorize",
		TokenURL:     "https://slack.com/api/oauth.v2.access",
		Scopes:       []string{"chat:write", "channels:read", "channels:history"},
		SetupHelp:    "Create a Slack app at api.slack.com/apps; client_id + client_secret are on its Basic Information page.",
	},
	{
		Name:         "github",
		DisplayName:  "GitHub",
		AuthorizeURL: "https://github.com/login/oauth/authorize",
		TokenURL:     "https://github.com/login/oauth/access_token",
		Scopes:       []string{"repo", "read:user"},
		SetupHelp:    "Register an OAuth app at github.com/settings/developers; copy Client ID and generate a Client secret.",
	},
	{
		Name:         "google",
		DisplayName:  "Google (Gmail / Sheets / Drive)",
		AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
		Scopes: []string{
			"https://www.googleapis.com/auth/gmail.send",
			"https://www.googleapis.com/auth/gmail.readonly",
			"https://www.googleapis.com/auth/spreadsheets",
			"https://www.googleapis.com/auth/drive.readonly",
		},
		AuthorizeExtras: map[string]string{
			// Required for refresh_token (Google's "first consent only"
			// quirk — without prompt=consent re-grants don't return
			// one and access expires in ~1h with no refresh path).
			"access_type": "offline",
			"prompt":      "consent",
		},
		SetupHelp: "Create OAuth credentials in Google Cloud Console (APIs & Services → Credentials → Create OAuth client ID, type Web). Add the daemon's /api/v1/oauth/google/callback URL as an authorized redirect URI.",
	},
	{
		Name:         "notion",
		DisplayName:  "Notion",
		AuthorizeURL: "https://api.notion.com/v1/oauth/authorize",
		TokenURL:     "https://api.notion.com/v1/oauth/token",
		Scopes:       nil, // Notion uses workspace-scope, no per-scope list.
		SetupHelp:    "Create a public integration at notion.so/my-integrations; OAuth client ID + secret appear under Capabilities → OAuth.",
	},
}

// providerDefault returns the defaults entry for name, or nil if the
// provider isn't in Hazy Flow's known catalogue.
func providerDefault(name string) *OAuthProviderDefault {
	for i := range KnownOAuthProviderDefaults {
		if KnownOAuthProviderDefaults[i].Name == name {
			return &KnownOAuthProviderDefaults[i]
		}
	}
	return nil
}

// toProvider lifts a defaults entry + credentials into the runnable
// OAuthProvider the registry holds.
func (d OAuthProviderDefault) toProvider(clientID, clientSecret string) OAuthProvider {
	return OAuthProvider{
		Name:            d.Name,
		AuthorizeURL:    d.AuthorizeURL,
		TokenURL:        d.TokenURL,
		Scopes:          d.Scopes,
		AuthorizeExtras: d.AuthorizeExtras,
		ClientID:        clientID,
		ClientSecret:    clientSecret,
	}
}
