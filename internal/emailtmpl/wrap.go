// Package emailtmpl holds the email-template library shared by the daemon
// (storage + HTTP CRUD + runtime resolution) and the email-sending drops
// (body wrapping). A template is a reusable HTML layout shell with a
// {{.Body}} placeholder; WrapBody injects an outgoing body into it.
package emailtmpl

import (
	"bytes"
	"html/template"
	"regexp"
)

// bodyPlaceholderRe matches the {{.Body}} action a template's HTML must
// contain — the point at which the drop's body is spliced in — tolerating
// incidental whitespace ("{{ .Body }}"). We validate its presence at save
// time (HasBodyPlaceholder) so a template that would silently drop the body
// is rejected before it can be referenced by a flow.
var bodyPlaceholderRe = regexp.MustCompile(`{{\s*\.Body\s*}}`)

// wrapData is the render context for a template shell.
type wrapData struct {
	// Body is the already-composed email body (the drop's HTML output). It is
	// author-controlled markup, so it is injected as template.HTML rather than
	// re-escaped — the shell wraps the body, it does not sanitise it.
	Body template.HTML
	// Subject is escaped normally; shells may show it (e.g. a preheader).
	Subject string
	// Logo is the org's logo URL (or empty), a plain string. Shells render it
	// via {{safeURL .Logo}}, which marks it trusted (like emailtheme's safeURL)
	// so a data: URI or https URL renders instead of being rewritten.
	Logo string
}

// funcMap is the template helper set every shell is parsed/executed with.
// safeURL marks a URL trusted; the value is operator/org config (an org-profile
// icon), never end-user input, so bypassing the src-scheme filter is safe.
var funcMap = template.FuncMap{
	"safeURL": func(s string) template.URL { return template.URL(s) },
}

// WrapBody renders shellHTML with body spliced into its {{.Body}} placeholder.
// subject and logo populate the optional {{.Subject}} and {{.Logo}} actions.
// A parse or execute error is returned to the caller (the drop turns it into a
// node failure); callers should have validated the shell at save time so this
// is rare at run time.
func WrapBody(shellHTML, body, subject, logo string) (string, error) {
	tmpl, err := template.New("emailtmpl").Funcs(funcMap).Parse(shellHTML)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, wrapData{
		Body:    template.HTML(body),
		Subject: subject,
		Logo:    logo,
	}); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// HasBodyPlaceholder reports whether shellHTML contains the {{.Body}} action,
// allowing for incidental whitespace inside the braces (e.g. "{{ .Body }}").
// Used to reject a template at save time that would drop the body. Returns
// false when the HTML doesn't parse — an unparseable shell is rejected anyway.
func HasBodyPlaceholder(shellHTML string) bool {
	// Parse with the same funcMap WrapBody uses, so a shell that legitimately
	// calls {{safeURL .Logo}} isn't rejected as "unparseable".
	if _, err := template.New("check").Funcs(funcMap).Parse(shellHTML); err != nil {
		return false
	}
	return bodyPlaceholderRe.MatchString(shellHTML)
}
