// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package caldavutil holds the CalDAV connection dance shared by the Calendar
// drops (drops/caldav — list events, create an event) and the "Test
// connection" verifier behind the integration page. Sibling to smtputil,
// imaputil and sftputil, split out for the same reason: how a calendar is
// discovered and which URL is actually talked to must NOT drift between
// running a flow and testing the credentials that flow will use.
//
// CalDAV is the vendor-neutral calendar: Fastmail, mailbox.org, iCloud,
// Nextcloud and a Radicale box of your own all speak it, so one connector
// reaches every calendar that isn't Google's.
package caldavutil

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// defaultTimeout bounds a request when the caller sets none.
const defaultTimeout = 30 * time.Second

// Config is one tenant's calendar account, parsed and defaulted from the
// ConnectionFields bundle on the integration page.
type Config struct {
	// URL is whatever the provider told the user to use. It may be a
	// discovery root ("https://caldav.fastmail.com/"), a principal, a
	// calendar-home collection, or one calendar's own path — resolveCalendar
	// copes with all of them, because a user cannot be expected to know
	// which of those they were handed.
	URL      string
	Username string
	Password string

	// Calendar names which calendar to use when the URL points at a
	// collection holding several. A display name ("Work") or a path both
	// work. Empty means "the only one", which is an error when there are
	// several — silence would be worse.
	Calendar string
}

// ConfigFromConn builds a Config from a stored connection map — the shape a
// connection verifier is handed.
func ConfigFromConn(conn map[string]string) (Config, error) {
	cfg := Config{
		URL:      strings.TrimSpace(conn["url"]),
		Username: strings.TrimSpace(conn["username"]),
		Password: conn["password"], // not trimmed: spaces can be part of it
		Calendar: strings.TrimSpace(conn["calendar"]),
	}
	if cfg.URL == "" {
		return Config{}, errors.New("enter the calendar server's address")
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil || parsed.Host == "" {
		return Config{}, errors.New(`that address doesn't look right — use the full URL your provider gave you, e.g. "https://caldav.fastmail.com/"`)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return Config{}, errors.New("the address must start with https:// (or http:// for a server on your own network)")
	}
	if cfg.Username == "" || strings.TrimSpace(cfg.Password) == "" {
		return Config{}, errors.New("enter the username and password for the calendar — on a provider with two-factor sign-in (Fastmail, iCloud), an app password")
	}
	return cfg, nil
}

// Client builds a CalDAV client for cfg.
//
// The transport is the shared SafeHTTPClient, so every request carries the
// SSRF guard and the operator's private-egress setting — without it a
// tenant-supplied URL could point the client, and the basic-auth header it
// sends, at cloud metadata or an internal host.
func Client(cfg Config, timeout time.Duration) (*caldav.Client, error) {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	http := hfnet.SafeHTTPClient(timeout, hfnet.PrivateEgressAllowed())
	authed := webdav.HTTPClientWithBasicAuth(http, cfg.Username, cfg.Password)
	c, err := caldav.NewClient(authed, cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("couldn't use that address: %w", err)
	}
	return c, nil
}

// ResolveCalendar works out which collection path to read and write.
//
// CalDAV discovery is the part users cannot be asked to do by hand. Providers
// hand out wildly different URLs — Fastmail a root, Nextcloud a per-user
// principal, iCloud a numeric home set — and the protocol expects a client to
// walk from whatever it was given to the calendar collections underneath.
// So: try the URL as a calendar home set first, then find the principal and
// its home set, and only then give up. Whatever succeeds, the result is a
// concrete collection path the query and the write both use, which is what
// keeps the verifier honest about what a run will do.
func ResolveCalendar(ctx context.Context, c *caldav.Client, cfg Config) (path string, err error) {
	cals, err := discover(ctx, c, cfg)
	if err != nil {
		return "", err
	}
	if len(cals) == 0 {
		return "", fmt.Errorf("no calendars found at %s — check the address, and that this account has a calendar", cfg.URL)
	}
	want := cfg.Calendar
	if want == "" {
		if len(cals) == 1 {
			return cals[0].Path, nil
		}
		// Naming them is the whole value of this error: the fix is to copy one
		// into the Calendar field, and a bare "ambiguous" would leave the user
		// guessing at spellings.
		return "", fmt.Errorf("this account has %d calendars (%s) — put the one you want in the Calendar field", len(cals), strings.Join(names(cals), ", "))
	}
	for _, cal := range cals {
		if strings.EqualFold(strings.TrimSpace(cal.Name), want) || cal.Path == want {
			return cal.Path, nil
		}
	}
	return "", fmt.Errorf("no calendar called %q here — this account has: %s", want, strings.Join(names(cals), ", "))
}

// discover walks from the configured URL to the calendar collections under
// it, trying the cheapest interpretation first.
func discover(ctx context.Context, c *caldav.Client, cfg Config) ([]caldav.Calendar, error) {
	// 1. The URL already names a calendar home set (or one calendar).
	if cals, err := c.FindCalendars(ctx, cfg.URL); err == nil && len(cals) > 0 {
		return cals, nil
	}
	// 2. Ask the server who we are, then where its calendars live. This is
	//    the path every major provider actually needs.
	principal, err := c.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s didn't accept the login, or isn't a CalDAV server — check the address and credentials (%w)", cfg.URL, err)
	}
	home, err := c.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return nil, fmt.Errorf("signed in, but couldn't find this account's calendars: %w", err)
	}
	cals, err := c.FindCalendars(ctx, home)
	if err != nil {
		return nil, fmt.Errorf("couldn't list the calendars: %w", err)
	}
	return cals, nil
}

func names(cals []caldav.Calendar) []string {
	out := make([]string, 0, len(cals))
	for _, c := range cals {
		n := strings.TrimSpace(c.Name)
		if n == "" {
			n = c.Path
		}
		out = append(out, n)
	}
	return out
}

// Verify is the "Test connection" probe: sign in, discover the calendars, and
// confirm the configured one is among them — without reading a single event.
// The calendar name is included because it is the field most likely to be
// quietly wrong, and a mistyped one otherwise fails per-run, deep inside a
// flow, where nothing points back at the integration page.
func Verify(ctx context.Context, cfg Config) error {
	c, err := Client(cfg, defaultTimeout)
	if err != nil {
		return err
	}
	_, err = ResolveCalendar(ctx, c, cfg)
	return err
}
