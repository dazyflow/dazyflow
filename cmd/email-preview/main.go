// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command email-preview renders every Dazyflow system email through the
// shared emailtheme and writes a single self-contained gallery HTML file you
// can open in a browser to review the design. It sends nothing.
//
//	go run ./cmd/email-preview            # writes ./email-preview.html
//	go run ./cmd/email-preview out.html   # writes a chosen path
//
// The sample copy mirrors what daemon/*.go actually sends (welcome,
// verification, password reset, org invite, signup invite, failure notify),
// so the preview is a faithful look at production mail — only the links and
// names are placeholders.
package main

import (
	"encoding/base64"
	"fmt"
	"html"
	"os"
	"strings"

	"git.sr.ht/~klahr/dazyflow/internal/emailtheme"
)

func main() {
	out := "email-preview.html"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}

	// Show the real hosted-PNG header in the preview by inlining the actual
	// asset as a data URI. Production sends point LogoURL at
	// {PublicBaseURL}/logo.png instead; if the asset isn't found here we just
	// leave LogoURL empty and the theme falls back to its inline SVG mark.
	logoURL := ""
	if png, err := os.ReadFile("web/public/logo.png"); err == nil {
		logoURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	}

	samples := []emailtheme.Content{
		{
			Subject:   "Welcome to Dazyflow",
			Preheader: "Your account is ready — build your first flow.",
			Eyebrow:   "Welcome",
			Heading:   "Your account is ready",
			Intro: []string{
				"Welcome to Dazyflow! Everything's set up and waiting for you.",
				"A flow automates a task for you — on a schedule, when a form is submitted, or when another app sends it data. Start from a template or describe what you want in plain words.",
			},
			Button: &emailtheme.Button{Label: "Build your first flow", URL: "https://app.dazyflow.example/welcome"},
			Outro:  []string{"Need a hand? Just reply to this email, or open the docs from inside the app."},
		},
		{
			Subject:   "Confirm your email",
			Preheader: "Confirm your address to finish setting up your account.",
			Eyebrow:   "Confirm your email",
			Heading:   "One quick step to finish",
			Intro:     []string{"Confirm your email address to finish setting up your Dazyflow account."},
			Button:    &emailtheme.Button{Label: "Verify email address", URL: "https://app.dazyflow.example/verify-email?email=you%40example.com&token=REDACTED"},
			Outro:     []string{"This link expires on 2 July 2026. If you didn't create a Dazyflow account, you can ignore this email."},
		},
		{
			Subject:   "Reset your Dazyflow password",
			Preheader: "Choose a new password for your account.",
			Eyebrow:   "Password reset",
			Heading:   "Reset your password",
			Intro:     []string{"We received a request to reset the password for your Dazyflow account."},
			Button:    &emailtheme.Button{Label: "Choose a new password", URL: "https://app.dazyflow.example/reset-password?email=you%40example.com&token=REDACTED"},
			Outro:     []string{"This link expires on 25 June 2026, 15:04 UTC. If you didn't request this, ignore this email — your password is unchanged."},
		},
		{
			Subject:    "You're invited to Dazyflow",
			Preheader:  "Maria invited you to join their organization.",
			Eyebrow:    "Invitation",
			Heading:    "You've been invited to join Acme Inc",
			Intro:      []string{"maria@acme.example invited you to join their organization on Dazyflow, where teams build and run automations together."},
			Button:     &emailtheme.Button{Label: "Accept invitation", URL: "https://app.dazyflow.example/invite/REDACTED"},
			Outro:      []string{"This invitation expires on 2 July 2026. If you weren't expecting it, you can ignore this email."},
			FooterNote: "You're receiving this because someone invited you to Dazyflow.",
		},
		{
			Subject:    "You're invited to Dazyflow",
			Preheader:  "Create your account to get started.",
			Eyebrow:    "Invitation",
			Heading:    "Create your Dazyflow account",
			Intro:      []string{"You've been invited to create an account on Dazyflow. Set a password and you're in."},
			Button:     &emailtheme.Button{Label: "Set your password", URL: "https://app.dazyflow.example/signup?email=you%40example.com&signup_invite=REDACTED"},
			Outro:      []string{"This link expires on 2 July 2026. If you weren't expecting it, you can ignore this email."},
			FooterNote: "You're receiving this because someone invited you to Dazyflow.",
		},
		{
			Subject:   `Flow "Daily sales report" failed`,
			Preheader: "A run of your flow failed and needs your attention.",
			Eyebrow:   "Run failed",
			Heading:   "A flow run needs your attention",
			Tone:      "danger",
			Intro:     []string{"Your flow “Daily sales report” failed on its last run. Here's what happened:"},
			Facts: []emailtheme.Fact{
				{Label: "Failed step", Value: "Send email"},
				{Label: "Error", Value: "the recipient address may be wrong (email_send_failed)"},
				{Label: "Finished at", Value: "25 June 2026, 06:00 UTC"},
			},
			Button: &emailtheme.Button{Label: "View run details", URL: "https://app.dazyflow.example/runs/REDACTED"},
			Outro:  []string{"This run won't retry on its own. Open it to see the full log and fix the cause."},
		},
	}

	var sections strings.Builder
	for _, c := range samples {
		c.LogoURL = logoURL
		body, err := emailtheme.Render(c)
		if err != nil {
			fmt.Fprintf(os.Stderr, "render %q: %v\n", c.Subject, err)
			os.Exit(1)
		}
		// Each email is a full HTML document; isolate it in an iframe via
		// srcdoc (attribute-escaped) so its styles can't leak into the
		// gallery chrome and it renders exactly as a client would parse it.
		fmt.Fprintf(&sections, `
    <section class="card">
      <div class="bar">
        <span class="dot"></span>
        <span class="subj">%s</span>
        <span class="tone tone-%s">%s</span>
      </div>
      <iframe loading="lazy" srcdoc="%s"></iframe>
    </section>`,
			html.EscapeString(c.Subject),
			toneClass(c.Tone),
			toneLabel(c.Tone),
			html.EscapeString(body),
		)
	}

	// strings.Replace, not fmt.Sprintf — the gallery CSS is full of "%"
	// (border-radius:50%, width:100%) that Sprintf would read as verbs.
	page := strings.Replace(galleryShell, "{{SECTIONS}}", sections.String(), 1)
	if err := os.WriteFile(out, []byte(page), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write %s: %v\n", out, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d emails)\n", out, len(samples))
}

func toneClass(t string) string {
	if t == "danger" {
		return "danger"
	}
	return "default"
}

func toneLabel(t string) string {
	if t == "danger" {
		return "transactional · danger"
	}
	return "transactional"
}

// galleryShell wraps the rendered emails. It's plain browser HTML (not
// email), so modern CSS and a tiny resize script are fine here. The script
// grows each iframe to fit its content so nothing scrolls or clips.
const galleryShell = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Dazyflow · system email preview</title>
<style>
  :root { color-scheme: light; }
  body { margin:0; background:#eceef5; color:#1b2233;
         font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif; }
  header { padding:32px 24px 8px; max-width:760px; margin:0 auto; }
  header h1 { margin:0 0 4px; font-size:22px; }
  header p { margin:0; color:#5b6577; font-size:14px; }
  main { max-width:760px; margin:0 auto; padding:16px 24px 64px; }
  .card { background:#fff; border:1px solid #dfe3ee; border-radius:12px;
          overflow:hidden; margin:20px 0; box-shadow:0 1px 3px rgba(20,13,48,.06); }
  .bar { display:flex; align-items:center; gap:10px; padding:12px 16px;
         border-bottom:1px solid #eef0f6; background:#fafbfe; }
  .dot { width:9px; height:9px; border-radius:50%; background:#6d28d9; flex:0 0 auto; }
  .subj { font-weight:600; font-size:14px; }
  .tone { margin-left:auto; font-size:11px; letter-spacing:.4px; text-transform:uppercase;
          padding:3px 9px; border-radius:999px; }
  .tone-default { background:#f0ebfd; color:#6d28d9; }
  .tone-danger { background:#fde8e8; color:#dc2626; }
  iframe { width:100%; border:0; display:block; background:#f4f5fa; }
</style>
</head>
<body>
  <header>
    <h1>Dazyflow — system email theme</h1>
    <p>Preview of every transactional email, rendered through the shared theme. Links and names are placeholders.</p>
  </header>
  <main>{{SECTIONS}}</main>
  <script>
    // Grow each isolated email frame to its content height.
    function fit(f){ try { f.style.height = (f.contentWindow.document.body.scrollHeight + 24) + 'px'; } catch(e){} }
    for (const f of document.querySelectorAll('iframe')) {
      f.addEventListener('load', () => fit(f));
    }
    window.addEventListener('resize', () => { for (const f of document.querySelectorAll('iframe')) fit(f); });
  </script>
</body>
</html>`
