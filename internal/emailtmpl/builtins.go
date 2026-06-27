// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package emailtmpl

import (
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
)

// BuiltinPrefix namespaces built-in template IDs. Org-created template names
// forbid ":" (enforced at save time), so a built-in ID can never collide with
// an org template name — the two namespaces are disjoint, and resolution can
// dispatch on this prefix alone.
const BuiltinPrefix = "builtin:"

// IsBuiltinID reports whether id refers to a global built-in template.
func IsBuiltinID(id string) bool {
	return strings.HasPrefix(id, BuiltinPrefix)
}

// builtins is the fixed, global, read-only catalog. Each shell wraps the
// drop's body via {{.Body}} and may surface the org logo via {{.Logo}}. The
// markup follows the same email-client-safe conventions as internal/emailtheme
// (nested tables, inline styles, 600px container, web-safe fonts).
var builtins = []core.EmailTemplate{
	{
		ID:   BuiltinPrefix + "plain",
		Name: "Plain",
		HTML: plainHTML,
	},
	{
		ID:   BuiltinPrefix + "branded",
		Name: "Branded",
		HTML: brandedHTML,
	},
	{
		ID:   BuiltinPrefix + "announcement",
		Name: "Announcement",
		HTML: announcementHTML,
	},
}

// BuiltinTemplates returns a copy of the global built-in catalog.
func BuiltinTemplates() []core.EmailTemplate {
	out := make([]core.EmailTemplate, len(builtins))
	copy(out, builtins)
	return out
}

// Builtin returns the built-in template with the given ID.
func Builtin(id string) (core.EmailTemplate, bool) {
	for _, t := range builtins {
		if t.ID == id {
			return t, true
		}
	}
	return core.EmailTemplate{}, false
}

// plain: a minimal, unstyled white card. The lightest wrapper — gives the body
// a centred, readable column without imposing branding.
const plainHTML = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head>
<body style="margin:0;padding:0;background-color:#f4f5fa;">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background-color:#f4f5fa;">
    <tr><td align="center" style="padding:32px 16px;">
      <table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0" style="width:600px;max-width:600px;">
        <tr><td style="background-color:#ffffff;padding:32px 40px;border:1px solid #e9ebf3;border-radius:12px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:15px;line-height:1.6;color:#1b2233;">
          {{.Body}}
        </td></tr>
      </table>
    </td></tr>
  </table>
</body>
</html>`

// branded: a white card with a gradient top bar, logo header, and footer —
// the same shell vocabulary as the system transactional emails.
const brandedHTML = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light only"></head>
<body style="margin:0;padding:0;background-color:#f4f5fa;">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background-color:#f4f5fa;">
    <tr><td align="center" style="padding:36px 16px;">
      <table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0" style="width:600px;max-width:600px;">
        <tr><td style="height:4px;line-height:4px;font-size:4px;background-color:#6d28d9;background-image:linear-gradient(90deg,#1fb6d4,#4c84e8,#9a7bf0,#eba7ee);border-radius:14px 14px 0 0;">&nbsp;</td></tr>
        {{if .Logo}}<tr><td style="background-color:#ffffff;padding:24px 40px 0;border-left:1px solid #e9ebf3;border-right:1px solid #e9ebf3;">
          <img src="{{safeURL .Logo}}" height="28" alt="" style="display:block;border:0;outline:none;text-decoration:none;max-height:28px;">
        </td></tr>{{end}}
        <tr><td style="background-color:#ffffff;padding:24px 40px;border-left:1px solid #e9ebf3;border-right:1px solid #e9ebf3;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:15px;line-height:1.6;color:#1b2233;">
          {{.Body}}
        </td></tr>
        <tr><td style="background-color:#ffffff;padding:18px 40px 26px;border-left:1px solid #e9ebf3;border-right:1px solid #e9ebf3;border-bottom:1px solid #e9ebf3;border-radius:0 0 14px 14px;">
          <div style="border-top:1px solid #eef0f6;height:1px;line-height:1px;font-size:1px;margin:0 0 14px;">&nbsp;</div>
          <p style="margin:0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:12px;line-height:1.6;color:#9298a8;">You're receiving this because you subscribed to updates.</p>
        </td></tr>
      </table>
    </td></tr>
  </table>
</body>
</html>`

// announcement: a bolder header band for marketing/announcement sends, with
// the logo and a coloured banner above the body.
const announcementHTML = `<!DOCTYPE html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light only"></head>
<body style="margin:0;padding:0;background-color:#f4f5fa;">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background-color:#f4f5fa;">
    <tr><td align="center" style="padding:36px 16px;">
      <table role="presentation" width="600" cellpadding="0" cellspacing="0" border="0" style="width:600px;max-width:600px;">
        <tr><td align="center" style="background-color:#6d28d9;background-image:linear-gradient(120deg,#4c84e8,#9a7bf0);padding:30px 40px;border-radius:14px 14px 0 0;">
          {{if .Logo}}<img src="{{safeURL .Logo}}" height="32" alt="" style="display:block;border:0;outline:none;text-decoration:none;max-height:32px;">{{end}}
        </td></tr>
        <tr><td style="background-color:#ffffff;padding:32px 40px;border-left:1px solid #e9ebf3;border-right:1px solid #e9ebf3;border-bottom:1px solid #e9ebf3;border-radius:0 0 14px 14px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:15px;line-height:1.6;color:#1b2233;">
          {{.Body}}
        </td></tr>
      </table>
    </td></tr>
  </table>
</body>
</html>`
