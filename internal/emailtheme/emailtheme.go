// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package emailtheme renders Dazyflow's transactional ("system") email into
// one professional, brand-consistent HTML layout: the welcome, email
// verification, password reset, organization invitation, signup invitation,
// and flow-failure notifications all share this shell.
//
// The markup is deliberately old-fashioned — nested tables, inline styles,
// a 600px fixed container, web-safe fonts, a "bulletproof" button — because
// that is the subset every mail client (Gmail, Outlook desktop, Apple Mail,
// the mobile apps) renders consistently. Modern CSS (fl«ex/grid, external
// stylesheets) is unreliable in email, so the <style> block carries only
// progressive-enhancement resets and a mobile breakpoint; every visual is
// also expressed inline so it survives a client that strips <style>.
//
// All caller-supplied text flows through html/template, so it is
// HTML-escaped automatically — an inviter's name, a flow name, or a remote
// error message can't inject markup into the email.
package emailtheme

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
)

// Button is the single primary call-to-action. URL is the destination; Label
// is the visible text. Optional — notification-only mails (none, currently)
// can omit it.
type Button struct {
	Label string
	URL   string
}

// Fact is one label/value row in the optional details table — used by the
// flow-failure mail to lay out "Failed step / Error / Finished at" without
// burying them in a paragraph.
type Fact struct {
	Label string
	Value string
}

// Content is everything the theme needs to render one email. Fields are
// composed top-to-bottom: eyebrow → heading → intro paragraphs → facts →
// button → outro paragraphs → footer note.
type Content struct {
	// Subject doubles as the document <title>. The mail transport sets the
	// real Subject header; this keeps the two in sync for previews.
	Subject string
	// Preheader is the hidden snippet inbox lists show next to the subject.
	// Kept short and specific ("Confirm your address to finish signing up").
	Preheader string
	// Eyebrow is a small uppercase kicker above the heading ("Confirm your
	// email", "Run failed"). Optional.
	Eyebrow string
	// Heading is the one-line headline. Required.
	Heading string
	// Intro paragraphs sit above the button; Outro paragraphs (expiry,
	// "if you didn't request this") sit below it, rendered quieter.
	Intro []string
	Facts []Fact
	// Button is the primary CTA. Optional.
	Button *Button
	Outro  []string
	// FooterNote overrides the default footer line when set (e.g. "You're
	// receiving this because someone invited you to Dazyflow").
	FooterNote string
	// LogoURL is an absolute URL to a hosted PNG of the brand mark
	// (typically {PublicBaseURL}/logo.png). When set, the header shows it as
	// an <img> — the reliable choice for real inboxes, since Gmail and others
	// strip inline SVG. When empty, the header falls back to the inline SVG
	// mark (renders in the browser preview and SVG-capable clients); the
	// "Dazyflow" wordmark shows either way.
	LogoURL string
	// Tone selects the accent. "" is the default brand purple; "danger"
	// tints the eyebrow and the details box red for failure notifications.
	Tone string
}

// Render returns the full HTML document for one email.
func Render(c Content) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, c); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// The brand mark: the two interlocking loops from web/public/logo.svg, drawn
// small for the header. Inline SVG renders in the browser preview and in
// SVG-capable clients (Apple Mail); clients that strip it (Gmail) still show
// the "Dazyflow" wordmark beside it. A production send that wants the mark
// everywhere should swap this for a hosted PNG at {PublicBaseURL}/logo.png.
const logoSVG template.HTML = `<svg width="26" height="26" viewBox="-10 -10 84 84" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="Dazyflow">
  <defs><linearGradient id="dz" x1="0" y1="64" x2="64" y2="0" gradientUnits="userSpaceOnUse">
    <stop offset="0" stop-color="#1fb6d4"/><stop offset=".4" stop-color="#4c84e8"/>
    <stop offset=".7" stop-color="#9a7bf0"/><stop offset="1" stop-color="#eba7ee"/>
  </linearGradient></defs>
  <g fill="none" stroke="url(#dz)" stroke-width="9">
    <circle cx="24" cy="40" r="16"/><circle cx="40" cy="24" r="16"/>
  </g>
</svg>`

// tmpl is parsed once at init; a parse error is a programmer bug, so panic.
var tmpl = template.Must(
	template.New("email").
		Funcs(template.FuncMap{
			"logo": func() template.HTML { return logoSVG },
			// safeURL marks the logo URL as trusted so html/template doesn't
			// rewrite it to "#ZgotmplZ". LogoURL is always operator config
			// ({PublicBaseURL}/logo.png) or a bundled preview asset — never
			// user input — so bypassing the src-scheme filter is safe and is
			// what lets a data: URI render in the preview.
			"safeURL": func(s string) template.URL { return template.URL(s) },
		}).
		Parse(htmlTemplate),
)

// htmlTemplate is the shared shell. Conventions baked in:
//   - 600px container, centred on a #f4f5fa page.
//   - White card with a thin gradient top bar (the brand colours).
//   - font stack: -apple-system / Segoe UI / Roboto / Helvetica / Arial.
//   - accent #6d28d9 (violet-700) for the button + links; #dc2626 for the
//     danger tone; ink #1b2233; muted #5b6577.
//   - the button is rendered both as a padded <a> and inside a bgcolor <td>
//     so Outlook (which ignores the <a> background) still shows a filled
//     button.
const htmlTemplate = `<!DOCTYPE html>
<html lang="en" xmlns="http://www.w3.org/1999/xhtml">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="X-UA-Compatible" content="IE=edge">
<meta name="color-scheme" content="light only">
<meta name="supported-color-schemes" content="light only">
<title>{{.Subject}}</title>
<!--[if mso]><style>* { font-family: Arial, sans-serif !important; }</style><![endif]-->
<style>
  body, table, td, a { -webkit-text-size-adjust: 100%; -ms-text-size-adjust: 100%; }
  table, td { mso-table-lspace: 0pt; mso-table-rspace: 0pt; }
  img { -ms-interpolation-mode: bicubic; border: 0; line-height: 100%; outline: none; text-decoration: none; }
  body { margin: 0 !important; padding: 0 !important; width: 100% !important; background-color: #f4f5fa; }
  a { color: #6d28d9; }
  .btn-a:hover { filter: brightness(1.06); }
  @media only screen and (max-width: 620px) {
    .container { width: 100% !important; }
    .px { padding-left: 24px !important; padding-right: 24px !important; }
  }
</style>
</head>
<body style="margin:0;padding:0;background-color:#f4f5fa;">
  <div style="display:none;max-height:0;overflow:hidden;mso-hide:all;opacity:0;font-size:1px;line-height:1px;color:#f4f5fa;">{{.Preheader}}&nbsp;&zwnj;&nbsp;&zwnj;&nbsp;&zwnj;&nbsp;&zwnj;&nbsp;&zwnj;&nbsp;&zwnj;</div>
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background-color:#f4f5fa;">
    <tr>
      <td align="center" style="padding:36px 16px;">
        <table role="presentation" class="container" width="600" cellpadding="0" cellspacing="0" border="0" style="width:600px;max-width:600px;">

          <!-- brand gradient bar -->
          <tr><td style="height:4px;line-height:4px;font-size:4px;background-color:#6d28d9;background-image:linear-gradient(90deg,#1fb6d4,#4c84e8,#9a7bf0,#eba7ee);border-radius:14px 14px 0 0;">&nbsp;</td></tr>

          <!-- header -->
          <tr><td class="px" style="background-color:#ffffff;padding:26px 40px 6px 40px;border-left:1px solid #e9ebf3;border-right:1px solid #e9ebf3;">
            <table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr>
              <td style="vertical-align:middle;padding-right:9px;line-height:1;">{{if .LogoURL}}<img src="{{safeURL .LogoURL}}" width="26" height="26" alt="Dazyflow" style="display:block;border:0;outline:none;text-decoration:none;">{{else}}{{logo}}{{end}}</td>
              <td style="vertical-align:middle;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:19px;font-weight:700;letter-spacing:.2px;color:#2d1b69;">Dazyflow</td>
            </tr></table>
          </td></tr>

          <!-- body -->
          <tr><td class="px" style="background-color:#ffffff;padding:14px 40px 4px 40px;border-left:1px solid #e9ebf3;border-right:1px solid #e9ebf3;">
            {{if .Eyebrow}}<div style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:12px;font-weight:700;letter-spacing:.8px;text-transform:uppercase;color:{{if eq .Tone "danger"}}#dc2626{{else}}#7c3aed{{end}};margin:8px 0 6px;">{{.Eyebrow}}</div>{{end}}
            <h1 style="margin:4px 0 14px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:23px;line-height:1.3;font-weight:700;color:#1b2233;">{{.Heading}}</h1>
            {{range .Intro}}<p style="margin:0 0 14px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:15px;line-height:1.6;color:#384152;">{{.}}</p>{{end}}

            {{with .Facts}}
            <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin:18px 0 4px;background-color:#f7f5fd;border-left:3px solid {{if eq $.Tone "danger"}}#dc2626{{else}}#6d28d9{{end}};border-radius:6px;">
              <tr><td style="padding:14px 18px;">
                {{range .}}<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0"><tr>
                  <td style="vertical-align:top;padding:3px 0;width:120px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:13px;color:#7a8294;">{{.Label}}</td>
                  <td style="vertical-align:top;padding:3px 0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:14px;color:#1b2233;font-weight:500;">{{.Value}}</td>
                </tr></table>{{end}}
              </td></tr>
            </table>
            {{end}}

            {{with .Button}}
            <table role="presentation" cellpadding="0" cellspacing="0" border="0" style="margin:24px 0 8px;"><tr>
              <td align="center" bgcolor="#6d28d9" style="border-radius:8px;">
                <a class="btn-a" href="{{.URL}}" target="_blank" rel="noopener" style="display:inline-block;padding:13px 30px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:15px;font-weight:600;line-height:1;color:#ffffff;text-decoration:none;border-radius:8px;background-color:#6d28d9;">{{.Label}}</a>
              </td>
            </tr></table>
            <p style="margin:0 0 14px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:13px;line-height:1.5;color:#8b93a7;">Or paste this link into your browser:<br><a href="{{.URL}}" target="_blank" rel="noopener" style="color:#6d28d9;word-break:break-all;">{{.URL}}</a></p>
            {{end}}

            {{range .Outro}}<p style="margin:0 0 12px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:13px;line-height:1.6;color:#8b93a7;">{{.}}</p>{{end}}
          </td></tr>

          <!-- footer -->
          <tr><td class="px" style="background-color:#ffffff;padding:18px 40px 26px;border-left:1px solid #e9ebf3;border-right:1px solid #e9ebf3;border-bottom:1px solid #e9ebf3;border-radius:0 0 14px 14px;">
            <div style="border-top:1px solid #eef0f6;height:1px;line-height:1px;font-size:1px;margin:0 0 16px;">&nbsp;</div>
            <p style="margin:0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:12px;line-height:1.6;color:#9298a8;">{{if .FooterNote}}{{.FooterNote}}{{else}}This is an automated message from Dazyflow. If it wasn't meant for you, you can safely ignore it.{{end}}</p>
            <p style="margin:8px 0 0;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;font-size:12px;color:#b3b8c6;">Dazyflow &middot; automated workflows</p>
          </td></tr>

        </table>
      </td>
    </tr>
  </table>
</body>
</html>`

// PlainText renders the same Content as the text/plain alternative of a
// multipart message.
//
// It exists because every caller used to hand-build that alternative next to
// the Content — the same facts, the same link, written twice — and the two
// drifted: one said "Failed step", the other "Failed step:  " with its own
// column alignment, and adding a fact to the HTML left the text version
// missing it. Worse, once the copy is translated, a hand-built twin doubles
// every string in the catalogue for no reader's benefit.
//
// The Preheader and Eyebrow are deliberately absent: the preheader exists to
// be the snippet an inbox list shows beside the subject, which a text body
// already is, and the eyebrow is a visual kicker above a heading that the
// heading itself repeats in words.
func PlainText(c Content) string {
	var b strings.Builder
	para := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			b.WriteString(s)
			b.WriteString("\n\n")
		}
	}
	para(c.Heading)
	for _, p := range c.Intro {
		para(p)
	}
	// Facts as an aligned block, so a long error value doesn't make the
	// labels unreadable. Width from the longest label present.
	if len(c.Facts) > 0 {
		width := 0
		for _, f := range c.Facts {
			if n := len([]rune(f.Label)); n > width {
				width = n
			}
		}
		for _, f := range c.Facts {
			pad := strings.Repeat(" ", width-len([]rune(f.Label)))
			fmt.Fprintf(&b, "%s:%s  %s\n", f.Label, pad, f.Value)
		}
		b.WriteString("\n")
	}
	if c.Button != nil && c.Button.URL != "" {
		fmt.Fprintf(&b, "%s:\n%s\n\n", strings.TrimSpace(c.Button.Label), c.Button.URL)
	}
	for _, p := range c.Outro {
		para(p)
	}
	para(c.FooterNote)
	return strings.TrimRight(b.String(), "\n") + "\n"
}
