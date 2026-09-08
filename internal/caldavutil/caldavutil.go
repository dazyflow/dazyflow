// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The CalDAV connection dance shared by the Calendar drops.
package caldavutil

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

const defaultTimeout = 30 * time.Second

type Config struct {
	// Whatever the provider told the user: a principal, a home set, or a calendar.
	URL      string
	Username string
	Password string

	Calendar string
}

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

// Which collection path to read and write, given any of those three shapes.
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
		// Naming the calendars IS the fix: the user copies one of them.
		return "", fmt.Errorf("this account has %d calendars (%s) — put the one you want in the Calendar field", len(cals), strings.Join(names(cals), ", "))
	}
	for _, cal := range cals {
		if strings.EqualFold(strings.TrimSpace(cal.Name), want) || cal.Path == want {
			return cal.Path, nil
		}
	}
	return "", fmt.Errorf("no calendar called %q here — this account has: %s", want, strings.Join(names(cals), ", "))
}

func discover(ctx context.Context, c *caldav.Client, cfg Config) ([]caldav.Calendar, error) {
	if cals, err := c.FindCalendars(ctx, cfg.URL); err == nil && len(cals) > 0 {
		return cals, nil
	}
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

func Verify(ctx context.Context, cfg Config) error {
	c, err := Client(cfg, defaultTimeout)
	if err != nil {
		return err
	}
	_, err = ResolveCalendar(ctx, c, cfg)
	return err
}

// A UID is unique within a calendar by the RFC, but not across calendars.
func FindEventPath(ctx context.Context, c *caldav.Client, dir, uid string) (string, error) {
	if uid == "" {
		return "", errors.New("no event id given")
	}
	direct := path.Join(dir, uid+".ics")
	if obj, err := c.GetCalendarObject(ctx, direct); err == nil && obj != nil {
		return obj.Path, nil
	}

	objects, err := c.QueryCalendar(ctx, dir, &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name:  "VCALENDAR",
			Comps: []caldav.CalendarCompRequest{{Name: "VEVENT", Props: []string{"UID"}}},
		},
		CompFilter: caldav.CompFilter{
			Name: "VCALENDAR",
			Comps: []caldav.CompFilter{{
				Name:  "VEVENT",
				Props: []caldav.PropFilter{{Name: "UID", TextMatch: &caldav.TextMatch{Text: uid}}},
			}},
		},
	})
	if err != nil {
		return "", fmt.Errorf("couldn't look the event up: %w", err)
	}
	// A server exposing several calendars can still answer twice.
	for _, obj := range objects {
		if obj.Data == nil {
			continue
		}
		for _, ev := range obj.Data.Events() {
			if got, err := ev.Props.Text(ical.PropUID); err == nil && got == uid {
				return obj.Path, nil
			}
		}
	}
	return "", nil
}

// Where THIS integration writes; another client may name the file differently.
func EventPath(dir, uid string) string { return path.Join(dir, uid+".ics") }
