// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// The deployment-invariant half: everything except the org's own client id.
type OAuthProviderDefault struct {
	Name            string
	AuthorizeURL    string
	TokenURL        string
	Scopes          []string
	AuthorizeExtras map[string]string
	DisplayName     string
	SetupHelp       string
	TokenAuthStyle  string
}

// Deployment-invariant, so an operator configures only credentials.
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
		DisplayName:  "Google (Gmail / Sheets / Drive / Calendar)",
		AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
		Scopes: []string{
			"https://www.googleapis.com/auth/gmail.send",
			"https://www.googleapis.com/auth/spreadsheets",
			"https://www.googleapis.com/auth/calendar.events",
			"https://www.googleapis.com/auth/calendar.readonly",
			// Google RESTRICTED scopes: an app requesting these needs verification, so the
			// set is kept to exactly what the shipped drops read.
			"https://www.googleapis.com/auth/gmail.readonly",
			"https://www.googleapis.com/auth/drive.readonly",
			"https://www.googleapis.com/auth/drive.file",
			"https://www.googleapis.com/auth/forms.responses.readonly",
			"https://www.googleapis.com/auth/forms.body.readonly",
		},
		AuthorizeExtras: map[string]string{
			"access_type": "offline",
			"prompt":      "consent",
			// Google grants scopes cumulatively, so a second connect for one drop must ask
			// only for what that drop needs, not re-request the lot.
			"include_granted_scopes": "true",
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
	{
		Name:            "fortnox",
		DisplayName:     "Fortnox",
		AuthorizeURL:    "https://apps.fortnox.se/oauth-v1/auth",
		TokenURL:        "https://apps.fortnox.se/oauth-v1/token",
		Scopes:          []string{"customer", "invoice", "companyinformation"},
		AuthorizeExtras: map[string]string{"access_type": "offline"},
		TokenAuthStyle:  "basic",
		SetupHelp:       "Create an app in the Fortnox Developer Portal (developer.fortnox.se); copy its Client ID and Client Secret and add the daemon's /api/v1/oauth/fortnox/callback URL as the redirect URI.",
	},
	{
		Name:           "spotify",
		DisplayName:    "Spotify",
		AuthorizeURL:   "https://accounts.spotify.com/authorize",
		TokenURL:       "https://accounts.spotify.com/api/token",
		Scopes:         []string{"user-follow-read"},
		TokenAuthStyle: "basic",
		SetupHelp:      "Create an app at developer.spotify.com/dashboard; add the daemon's /api/v1/oauth/spotify/callback URL as a redirect URI. A Spotify app stays in development mode unless its owner qualifies for extended quota (a registered business with 250k monthly users), which means at most 5 listeners — each allowlisted by hand in the dashboard — and the app owner needs Spotify Premium.",
	},
}

// Keyed on the Integration label, which is what a connect knows.
var googleScopeGroups = map[string][]string{
	"Gmail": {
		"https://www.googleapis.com/auth/gmail.send",
		"https://www.googleapis.com/auth/gmail.readonly",
	},
	"Google Sheets": {
		"https://www.googleapis.com/auth/spreadsheets",
		"https://www.googleapis.com/auth/drive.readonly",
	},
	"Google Calendar": {
		"https://www.googleapis.com/auth/calendar.events",
		"https://www.googleapis.com/auth/calendar.readonly",
	},
	"Google Drive": {
		"https://www.googleapis.com/auth/drive.readonly",
		"https://www.googleapis.com/auth/drive.file",
	},
	"Google Forms": {
		"https://www.googleapis.com/auth/forms.responses.readonly",
		"https://www.googleapis.com/auth/forms.body.readonly",
		"https://www.googleapis.com/auth/drive.metadata.readonly",
	},
}

// The MINIMAL set: asking for more than the drop needs is what triggers review.
func scopeSubsetForIntegration(provider, integration string) []string {
	if provider != "google" || integration == "" {
		return nil
	}
	return googleScopeGroups[integration]
}

func scopeGroupsForProvider(provider string) map[string][]string {
	if provider == "google" {
		return googleScopeGroups
	}
	return nil
}

// Only Google grants cumulatively; the others replace the granted set.
func providerUsesIncrementalScopes(provider string) bool {
	return provider == "google"
}

func providerDefault(name string) *OAuthProviderDefault {
	for i := range KnownOAuthProviderDefaults {
		if KnownOAuthProviderDefaults[i].Name == name {
			return &KnownOAuthProviderDefaults[i]
		}
	}
	return nil
}

func (d OAuthProviderDefault) toProvider(clientID, clientSecret string) OAuthProvider {
	return OAuthProvider{
		Name:            d.Name,
		AuthorizeURL:    d.AuthorizeURL,
		TokenURL:        d.TokenURL,
		Scopes:          d.Scopes,
		AuthorizeExtras: d.AuthorizeExtras,
		ClientID:        clientID,
		ClientSecret:    clientSecret,
		TokenAuthStyle:  d.TokenAuthStyle,
	}
}
