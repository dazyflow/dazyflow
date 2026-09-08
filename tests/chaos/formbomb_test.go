// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
)

func TestHostedForm_FieldCountIsCapped(t *testing.T) {
	const fields = 100_000

	h := newHarness(t)
	names := make([]any, 0, fields)
	for i := range fields {
		names = append(names, "f"+itoa(i))
	}
	g := graph("formbomb", []core.Node{{
		ID:     "in",
		Module: "form_input",
		Params: map[string]any{
			"form_fields": names,
			"form_title":  "Contact",
		},
	}}, nil)
	if err := h.publish(t, g); err != nil {
		t.Logf("refused at the save gate: %v", firstLine(err))
		return
	}
	t.Logf("stored: a flow whose hosted form declares %d fields (graph %d bytes)",
		fields, graphJSONBytes(g))

	wh := daemon.NewWebhookListener(h.svc)
	mux := http.NewServeMux()
	mux.HandleFunc("/form/", func(rw http.ResponseWriter, r *http.Request) {
		daemon.ServeFormForTest(wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	var (
		size    int64
		elapsed time.Duration
	)
	withinDeadline(t, "anonymous GET of the hosted form", 60*time.Second, func() {
		start := time.Now()
		resp, err := http.Get(ts.URL + "/form/acme/ws1/formbomb")
		if err != nil {
			t.Errorf("GET: %v", err)
			return
		}
		defer resp.Body.Close()
		size, err = io.Copy(io.Discard, resp.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		elapsed = time.Since(start)
	})
	t.Logf("one anonymous GET returned %d bytes in %s (%.0fx the graph)",
		size, elapsed.Round(time.Millisecond), float64(size)/float64(graphJSONBytes(g)))
	if size > 1<<20 {
		t.Errorf("FINDING: an unauthenticated GET of one hosted form returned %d bytes "+
			"(%d declared fields, no cap on the rendered field count)", size, fields)
	}
}

func TestHostedForm_FieldNameLengthIsCapped(t *testing.T) {
	const (
		fields  = core.MaxHostedFormFields // sit exactly on the count ceiling
		nameLen = 300_000                  // 50 x 300 KB stays inside MaxGraphBytes
	)
	h := newHarness(t)
	names := make([]any, 0, fields)
	for i := range fields {
		names = append(names, strings.Repeat("n", nameLen)+itoa(i))
	}
	g := graph("formnamebomb", []core.Node{{
		ID:     "in",
		Module: "form_input",
		Params: map[string]any{
			"form_fields": names,
			"form_title":  strings.Repeat("T", nameLen),
		},
	}}, nil)
	if err := h.publish(t, g); err != nil {
		t.Logf("refused at the save gate: %v", firstLine(err))
		return
	}
	t.Logf("stored: a hosted form declaring %d fields of %d chars each (graph %d bytes)",
		fields, nameLen, graphJSONBytes(g))

	wh := daemon.NewWebhookListener(h.svc)
	mux := http.NewServeMux()
	mux.HandleFunc("/form/", func(rw http.ResponseWriter, r *http.Request) {
		daemon.ServeFormForTest(wh, rw, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	var (
		size    int64
		elapsed time.Duration
	)
	withinDeadline(t, "anonymous GET of the hosted form", 120*time.Second, func() {
		start := time.Now()
		resp, err := http.Get(ts.URL + "/form/acme/ws1/formnamebomb")
		if err != nil {
			t.Errorf("GET: %v", err)
			return
		}
		defer resp.Body.Close()
		size, err = io.Copy(io.Discard, resp.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		elapsed = time.Since(start)
	})
	t.Logf("one anonymous GET returned %d bytes in %s (%.1fx the graph)",
		size, elapsed.Round(time.Millisecond), float64(size)/float64(graphJSONBytes(g)))
	if size > 1<<20 {
		t.Errorf("FINDING: an unauthenticated GET of one hosted form returned %d bytes "+
			"(%d fields named %d chars each, no cap on the rendered name length)",
			size, fields, nameLen)
	}
}
