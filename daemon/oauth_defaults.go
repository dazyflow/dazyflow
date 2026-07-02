// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// OAuthProviderDefault is the URLs+scopes side of an OAuthProvider —
// everything except the per-deployment ClientID/ClientSecret. The
// defaults table below is the canonical "what services can a Dazy
// Flow install talk to OAuth-wise", used both at boot (env-var
// hydration in cmd/dzd) and at admin runtime (so the UI can list
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
	// TokenAuthStyle selects how client credentials reach the token
	// endpoint: "" (client_secret_post, the default) or "basic"
	// (client_secret_basic). See OAuthProvider.TokenAuthStyle.
	TokenAuthStyle string
}

// KnownOAuthProviderDefaults is the deployment-invariant catalogue of
// OAuth providers Dazyflow's drops can use. Order matters: it's the
// order admin UI rows render in.
//
// The Google entry requests only non-restricted scopes by default
// (gmail.send + spreadsheets). The restricted readonly scopes are parked
// (see the Google entry) because Google blocks restricted-scope consent
// on an unverified app, which otherwise fails the entire connect.
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
			// Sensitive scopes: send mail + read/write Sheets + Calendar
			// events (calendar.events reads/writes events; calendar.readonly
			// lists calendars and reads events).
			"https://www.googleapis.com/auth/gmail.send",
			"https://www.googleapis.com/auth/spreadsheets",
			"https://www.googleapis.com/auth/calendar.events",
			"https://www.googleapis.com/auth/calendar.readonly",
			// Restricted scopes: gmail.readonly powers gmail_search /
			// gmail_get; drive.readonly powers sheets_export_pdf;
			// forms.responses.readonly + forms.body.readonly power the
			// google_form_trigger (read new responses, plus the question
			// titles used to key each answer).
			//
			// These grant freely on an INTERNAL Workspace app (no
			// verification). On an EXTERNAL app, Google blocks restricted-
			// scope consent until the app passes its security assessment —
			// so a multi-org / External deployment must get the app verified
			// (or drop these) before outside companies can connect.
			"https://www.googleapis.com/auth/gmail.readonly",
			"https://www.googleapis.com/auth/drive.readonly",
			// drive.file grants write access to files the app creates/opens —
			// least privilege for drive_upload (no access to the user's other
			// files). drive.readonly above powers drive_list / drive_download.
			"https://www.googleapis.com/auth/drive.file",
			"https://www.googleapis.com/auth/forms.responses.readonly",
			"https://www.googleapis.com/auth/forms.body.readonly",
		},
		AuthorizeExtras: map[string]string{
			// Required for refresh_token (Google's "first consent only"
			// quirk — without prompt=consent re-grants don't return
			// one and access expires in ~1h with no refresh path).
			"access_type": "offline",
			"prompt":      "consent",
			// Incremental authorization: when a connect requests only one
			// service's scopes (see scopeSubsetForIntegration), Google
			// MERGES the new grant with any already held instead of
			// replacing it. So connecting for Sheets after Gmail keeps the
			// Gmail grant, and the user only ever sees consent for the
			// service they're actually connecting.
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
		Name:         "fortnox",
		DisplayName:  "Fortnox",
		AuthorizeURL: "https://apps.fortnox.se/oauth-v1/auth",
		TokenURL:     "https://apps.fortnox.se/oauth-v1/token",
		// Scopes are per-resource. This set covers the shipped drops:
		// customer (create/list), invoice (create + the paid-invoice poll),
		// and companyinformation (a cheap read to verify a connection).
		// Add more here as the connector grows (article, order, bookkeeping…).
		Scopes: []string{"customer", "invoice", "companyinformation"},
		// Fortnox only returns a refresh_token when offline access is
		// requested — without it the access token dies in ~1h with no
		// refresh path, exactly like Google.
		AuthorizeExtras: map[string]string{"access_type": "offline"},
		// Fortnox's token endpoint demands client_secret_basic — credentials
		// in an HTTP Basic header, rejected if also sent in the body.
		TokenAuthStyle: "basic",
		SetupHelp:      "Create an app in the Fortnox Developer Portal (developer.fortnox.se); copy its Client ID and Client Secret and add the daemon's /api/v1/oauth/fortnox/callback URL as the redirect URI.",
	},
}

// googleScopeGroups maps a connector's Integration label (Manifest.Integration)
// to the minimal Google scopes that integration needs. It drives incremental
// authorization: connecting Google for one integration requests only its
// scopes, so a user wiring up Gmail never sees a Sheets/Forms consent screen.
// include_granted_scopes=true (on the google provider's AuthorizeExtras)
// merges each grant with any already held, so connecting for a second service
// tops up rather than replaces.
//
// Keyed by the same labels the drops set (drops/gmail, drops/sheets,
// drops/trigger/gform) and the Apps page passes back as ?integration=.
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
		// list + download read with drive.readonly; upload writes with the
		// least-privilege drive.file (app-created files only).
		"https://www.googleapis.com/auth/drive.readonly",
		"https://www.googleapis.com/auth/drive.file",
	},
	"Google Forms": {
		"https://www.googleapis.com/auth/forms.responses.readonly",
		"https://www.googleapis.com/auth/forms.body.readonly",
		// drive.metadata.readonly powers the form picker: the Forms API has
		// no "list my forms", so the dropdown lists form files via Drive.
		// Metadata-only (names + ids, no content) — least privilege.
		"https://www.googleapis.com/auth/drive.metadata.readonly",
	},
}

// scopeSubsetForIntegration returns the minimal scopes to request when
// connecting `provider` for a specific `integration`. Returns nil when the
// provider has no scope groups or the integration is unknown/empty — callers
// then fall back to the provider's full scope list (request-everything, the
// legacy behaviour, still available for a deliberate "connect all" flow).
func scopeSubsetForIntegration(provider, integration string) []string {
	if provider != "google" || integration == "" {
		return nil
	}
	return googleScopeGroups[integration]
}

// scopeGroupsForProvider returns the integration→scopes map for a provider
// that authorizes incrementally (only Google today), or nil. The
// /admin/google accounts endpoint inverts it to report, per connected
// account, which services its grant covers — so the scope→service mapping
// stays single-source with the connect path's scopeSubsetForIntegration.
func scopeGroupsForProvider(provider string) map[string][]string {
	if provider == "google" {
		return googleScopeGroups
	}
	return nil
}

// providerUsesIncrementalScopes reports whether a provider grants scopes
// incrementally (per integration) rather than all-at-once. For such a
// provider a connected account is never "globally stale" merely for lacking
// some other service's scopes — that scope is topped up the moment the user
// connects for that service, so the all-scopes staleness pill would be a
// false alarm.
func providerUsesIncrementalScopes(provider string) bool {
	return provider == "google"
}

// providerDefault returns the defaults entry for name, or nil if the
// provider isn't in Dazyflow's known catalogue.
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
		TokenAuthStyle:  d.TokenAuthStyle,
	}
}
