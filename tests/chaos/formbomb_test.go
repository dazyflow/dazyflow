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

// The hosted form is the one page in the product an unauthenticated stranger
// can GET: /form/<tenant>/<workspace>/<id>, no token, possession of the link is
// the capability. Its field list comes straight off the webhook_input step's
// form_fields param, and it is rendered in full on every request.
//
// An inbound SUBMISSION is capped (daemon.maxFormFields, 50). The RENDER is
// not: nothing counts form_fields at the save gate or at render time, so the
// only ceiling is the 16 MiB graph budget the names are charged against — and
// each name comes back as a label, an input, an id and a for=, so the page
// amplifies what the graph stores several times over. One saved flow then
// answers every anonymous GET with it.
func TestHostedForm_FieldCountIsCapped(t *testing.T) {
	const fields = 100_000

	h := newHarness(t)
	names := make([]any, 0, fields)
	for i := range fields {
		names = append(names, "f"+itoa(i))
	}
	g := graph("formbomb", []core.Node{{
		ID:     "in",
		Module: "webhook_input",
		Params: map[string]any{
			"public_form": true,
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

// The same amplifier with the other half of the field list unbounded. Capping
// the COUNT at MaxHostedFormFields left the LENGTH of a name free, and a name
// has no natural size — so the attack rebuilds itself inside the cap: 50
// declared fields, each named 300 KB, is a ~14 MiB graph that sits inside
// MaxGraphBytes and comes back four times over on every anonymous GET (for=,
// the label text, id= and name=).
//
// This is the count/length confusion TestIdentifierBytes_AreCapped is about,
// on the one endpoint that needs no credential.
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
		Module: "webhook_input",
		Params: map[string]any{
			"public_form": true,
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
