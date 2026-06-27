// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

// EmailTemplate is a reusable HTML layout shell that the email-sending drops
// (email_send, gmail_send_email) wrap an outgoing body in. Unlike a message
// the user types per-flow, a template carries only the look — logo, header,
// footer, button styling — and exposes a {{.Body}} placeholder the drop fills
// at run time. The drop stores only the template's ID in its params and
// resolves the HTML fresh on every run (a live reference), so editing a
// template updates every flow that uses it.
//
// Built-in templates are global and read-only (ID namespaced "builtin:");
// org-created templates are tenant-private, stored as JSON under the reserved
// "emailtmpl." namespace of the encrypted secret store. For an org template
// the ID equals the user-chosen Name.
type EmailTemplate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// HTML is the shell markup. It must contain the {{.Body}} action; the
	// optional {{.Subject}} and {{.Logo}} actions are also available.
	HTML string `json:"html"`
}
