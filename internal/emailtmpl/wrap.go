// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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

type wrapData struct {
	// Body is the already-composed email body (the drop's HTML output). It is
	// author-controlled markup, so it is injected as template.HTML rather than
	// re-escaped — the shell wraps the body, it does not sanitise it.
	Body    template.HTML
	Subject string
	Logo    string
}

// funcMap is the template helper set every shell is parsed/executed with.
// safeURL marks a URL trusted; the value is operator/org config (an org-profile
// icon), never end-user input, so bypassing the src-scheme filter is safe.
var funcMap = template.FuncMap{
	"safeURL": func(s string) template.URL { return template.URL(s) },
}

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
	if _, err := template.New("check").Funcs(funcMap).Parse(shellHTML); err != nil {
		return false
	}
	return bodyPlaceholderRe.MatchString(shellHTML)
}
