// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// writeSharedJSONPair frames the envelope by hand to avoid encoding a big
// value twice. That is only safe if it is byte-for-byte what the obvious
// map form produced, which is what this asserts — against the real
// manifest type, so a field's own custom marshaling is covered too.
func TestWriteSharedJSONPair_ByteIdenticalToMap(t *testing.T) {
	cases := map[string][]core.Manifest{
		"empty": {},
		"nil":   nil,
		"typical": {
			{ID: "http_request", Label: "HTTP Request", Integration: "http",
				ParamsSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}}}`)},
			{ID: "slack_post", Label: `Post to "Slack"`, Integration: "slack"},
		},
	}
	for name, mans := range cases {
		t.Run(name, func(t *testing.T) {
			want := httptest.NewRecorder()
			writeJSON(want, 200, map[string]any{"drops": mans, "modules": mans})

			got := httptest.NewRecorder()
			writeSharedJSONPair(got, "drops", "modules", mans)

			if got.Body.String() != want.Body.String() {
				t.Fatalf("bytes differ\n got: %s\nwant: %s", got.Body.String(), want.Body.String())
			}
			if ct := got.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("Content-Type = %q", ct)
			}
			// And it must still be the shape clients parse.
			var body struct {
				Drops   []core.Manifest `json:"drops"`
				Modules []core.Manifest `json:"modules"`
			}
			if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(body.Drops) != len(mans) || len(body.Modules) != len(mans) {
				t.Fatalf("keys carry %d/%d, want %d each",
					len(body.Drops), len(body.Modules), len(mans))
			}
		})
	}
}

// The pooled buffer is shared across goroutines; a leaked reference would
// show up as interleaved bodies.
func TestWriteSharedJSONPair_Concurrent(t *testing.T) {
	mans := []core.Manifest{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}
	want := httptest.NewRecorder()
	writeSharedJSONPair(want, "drops", "modules", mans)
	expect := want.Body.String()

	done := make(chan string, 16)
	for range 16 {
		go func() {
			rw := httptest.NewRecorder()
			writeSharedJSONPair(rw, "drops", "modules", mans)
			done <- rw.Body.String()
		}()
	}
	for range 16 {
		if got := <-done; got != expect {
			t.Fatalf("concurrent body differs:\n got: %s\nwant: %s", got, expect)
		}
	}
}
