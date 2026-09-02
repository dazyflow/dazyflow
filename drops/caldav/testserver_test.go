// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package caldav

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-ical"
	dav "github.com/emersion/go-webdav/caldav"

	"github.com/dazyflow/dazyflow/core"
)

const (
	testUser = "ada@example.test"
	testPass = "app-password"
	// go-webdav's CalDAV handler derives what a path IS from its DEPTH:
	// 1 = principal, 2 = calendar home set, 3 = calendar, 4 = object. So the
	// test paths have to sit at those depths for its own routing to work.
	// That is a constraint of this test server, not of CalDAV — real servers
	// lay out paths however they like, which is exactly why the client does a
	// discovery walk instead of assuming a shape.
	principal = "/ada/"
	homeSet   = "/ada/calendars/"
)

// memBackend is an in-memory CalDAV backend. A real server rather than canned
// HTTP because everything that can go wrong in CalDAV lives in the XML on the
// wire — the discovery walk, the time-range REPORT, the shape of a PUT. A
// hand-stubbed responder would assert our own assumptions back at us.
type memBackend struct {
	mu        sync.Mutex
	calendars []dav.Calendar
	objects   map[string]*dav.CalendarObject // path -> object
}

func newBackend(calNames ...string) *memBackend {
	b := &memBackend{objects: map[string]*dav.CalendarObject{}}
	for _, name := range calNames {
		b.calendars = append(b.calendars, dav.Calendar{
			Path:                  path.Join(homeSet, strings.ToLower(name)) + "/",
			Name:                  name,
			SupportedComponentSet: []string{"VEVENT"},
			MaxResourceSize:       4096,
		})
	}
	return b
}

func (b *memBackend) CurrentUserPrincipal(ctx context.Context) (string, error) {
	return principal, nil
}

func (b *memBackend) CalendarHomeSetPath(ctx context.Context) (string, error) {
	return homeSet, nil
}

func (b *memBackend) ListCalendars(ctx context.Context) ([]dav.Calendar, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]dav.Calendar, len(b.calendars))
	copy(out, b.calendars)
	return out, nil
}

func (b *memBackend) GetCalendar(ctx context.Context, p string) (*dav.Calendar, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.calendars {
		if b.calendars[i].Path == p {
			cal := b.calendars[i]
			return &cal, nil
		}
	}
	return nil, fmt.Errorf("no calendar %q", p)
}

func (b *memBackend) CreateCalendar(ctx context.Context, cal *dav.Calendar) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calendars = append(b.calendars, *cal)
	return nil
}

func (b *memBackend) GetCalendarObject(ctx context.Context, p string, req *dav.CalendarCompRequest) (*dav.CalendarObject, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if obj, ok := b.objects[p]; ok {
		return obj, nil
	}
	return nil, fmt.Errorf("no object %q", p)
}

func (b *memBackend) ListCalendarObjects(ctx context.Context, p string, req *dav.CalendarCompRequest) ([]dav.CalendarObject, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []dav.CalendarObject
	for objPath, obj := range b.objects {
		if strings.HasPrefix(objPath, p) {
			out = append(out, *obj)
		}
	}
	return out, nil
}

// QueryCalendarObjects applies the time-range filter the same way a real
// server does: an event is in the window when it starts before the end and
// ends after the start. Implemented rather than ignored, so the window tests
// are actually testing a window.
func (b *memBackend) QueryCalendarObjects(ctx context.Context, p string, query *dav.CalendarQuery) ([]dav.CalendarObject, error) {
	all, err := b.ListCalendarObjects(ctx, p, nil)
	if err != nil {
		return nil, err
	}
	var start, end time.Time
	for _, comp := range query.CompFilter.Comps {
		if comp.Name == "VEVENT" {
			start, end = comp.Start, comp.End
		}
	}
	var out []dav.CalendarObject
	for _, obj := range all {
		if obj.Data == nil {
			continue
		}
		keep := false
		for _, ev := range obj.Data.Events() {
			evStart, _ := ev.DateTimeStart(time.UTC)
			evEnd, _ := ev.DateTimeEnd(time.UTC)
			if !start.IsZero() && !evEnd.After(start) {
				continue
			}
			if !end.IsZero() && !evStart.Before(end) {
				continue
			}
			keep = true
		}
		if keep {
			out = append(out, obj)
		}
	}
	return out, nil
}

func (b *memBackend) PutCalendarObject(ctx context.Context, p string, cal *ical.Calendar, opts *dav.PutCalendarObjectOptions) (*dav.CalendarObject, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	obj := &dav.CalendarObject{Path: p, ModTime: time.Now(), ETag: "etag", Data: cal}
	b.objects[p] = obj
	return obj, nil
}

func (b *memBackend) DeleteCalendarObject(ctx context.Context, p string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.objects, p)
	return nil
}

// put plants an event on the backend directly, for the listing tests.
func (b *memBackend) put(t *testing.T, calendar, uid, summary string, start, end time.Time) {
	t.Helper()
	cal := buildCalendar(uid, summary, "", "", "", start, end)
	p := path.Join(homeSet, calendar, uid+".ics")
	if _, err := b.PutCalendarObject(context.Background(), p, cal, nil); err != nil {
		t.Fatal(err)
	}
}

// count returns how many objects the backend holds — the assertion for
// "did the write land, and exactly once".
func (b *memBackend) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.objects)
}

// startCalDAV serves the backend over HTTP with basic auth, and returns its
// base URL.
func startCalDAV(t *testing.T, b *memBackend) string {
	t.Helper()
	handler := &dav.Handler{Backend: b}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != testUser || pass != testPass {
			w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/"
}

// job is a job wired to the test server the way the engine wires a real one:
// the connection fields arrive as params (injectConnectionDefaults), with the
// per-event fields alongside them.
func job(t *testing.T, url string, p map[string]any) core.Job {
	t.Helper()
	full := map[string]any{
		"url":      url,
		"username": testUser,
		"password": testPass,
	}
	for k, v := range p {
		full[k] = v
	}
	return core.Job{
		ID: "job-1", GraphID: "graph-1", NodeID: "node-1", Tenant: "tenant-1",
		Params: full,
	}
}
