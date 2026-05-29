// Worked example: the "google" integration. In production this is the only
// content of the `google` integration repo, tagged v1.0.0. Installing it makes
// the platform support "google", which Gmail / Sheets / Drive / Calendar drops
// depend on via requiresConnections:[{kind:"oauth", name:"google"}].
//
// defineIntegration is the identity helper from the SDK (types-only here; a
// trivial runtime `(m) => m` once the SDK is published). The manifest is pure
// data — it reduces to a JSON object the daemon loads with a plain unmarshal.
import { defineIntegration } from "../hazyflow-integration";

export default defineIntegration({
  id: "google", // drops match this in requiresConnections.name
  version: "1.0.0", // = this repo's git tag
  label: "Google",
  summary:
    "Connect Google accounts so Gmail, Sheets, Drive, and Calendar drops can act on your behalf.",
  icon: "google",
  brandLogo: "/brands/google.svg",
  docsUrl: "https://console.cloud.google.com/apis/credentials",

  // The provider recipe — what registerOAuthProviders() hardcodes today,
  // lifted into data. No secrets: this whole object is public + signed.
  auth: {
    kind: "oauth2",
    authorizeUrl: "https://accounts.google.com/o/oauth2/v2/auth",
    tokenUrl: "https://oauth2.googleapis.com/token",
    usePKCE: true,
    refreshable: true,
    clientAuth: "body",
    // Without these Google never returns a refresh_token, so connections would
    // silently die after an hour.
    authorizeParams: { access_type: "offline", prompt: "consent" },
    scopes: [
      "https://www.googleapis.com/auth/gmail.send",
      "https://www.googleapis.com/auth/gmail.readonly",
      "https://www.googleapis.com/auth/spreadsheets",
    ],
  },

  // What the OPERATOR supplies at install — drives the admin GUI form, the way
  // a drop's params_schema drives the node form via SchemaForm.
  setup: [
    {
      key: "client_id",
      label: "OAuth client ID",
      type: "text",
      required: true,
      help: "Google Cloud Console → Credentials → OAuth client ID (Web application).",
    },
    {
      key: "client_secret",
      label: "OAuth client secret",
      type: "secret",
      required: true,
    },
    {
      key: "redirect_uri",
      label: "Authorized redirect URI",
      type: "display",
      value: "{publicBaseUrl}/api/v1/oauth/{id}/callback",
      help: "Add this exact URL under 'Authorized redirect URIs' in the console.",
    },
  ],
});
