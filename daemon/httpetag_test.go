// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func catalogPairFixture() []core.Manifest {
	// Big enough to clear gzipMinSize, so the compressed path is the one
	// under test rather than the small-body fallback.
	mans := make([]core.Manifest, 0, 40)
	for i := range 40 {
		mans = append(mans, core.Manifest{
			ID:    "drop_" + strconv.Itoa(i),
			Label: "Drop number " + strconv.Itoa(i) + " with a label long enough to matter",
			ParamsSchema: json.RawMessage(
				`{"type":"object","properties":{"url":{"type":"string","description":"the endpoint to call"}}}`),
		})
	}
	return mans
}

func pairRequest(t *testing.T, mans []core.Manifest, acceptEncoding, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/drops", nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rw := httptest.NewRecorder()
	writeSharedJSONPairCached(rw, req, "drops", "modules", mans, true)
	return rw
}

// The cached writer must put the same bytes on the wire as the plain one —
// the response cache is a CPU optimisation, not a format change.
func TestWriteSharedJSONPairCached_SameBytesAsPlain(t *testing.T) {
	for name, mans := range map[string][]core.Manifest{
		"empty":   {},
		"nil":     nil,
		"catalog": catalogPairFixture(),
	} {
		t.Run(name, func(t *testing.T) {
			want := httptest.NewRecorder()
			writeJSON(want, http.StatusOK, map[string]any{"drops": mans, "modules": mans})

			// Uncompressed.
			got := pairRequest(t, mans, "", "")
			if got.Body.String() != want.Body.String() {
				t.Fatalf("plain bytes differ\n got: %s\nwant: %s", got.Body.String(), want.Body.String())
			}
			if n, _ := strconv.Atoi(got.Header().Get("Content-Length")); n != want.Body.Len() {
				t.Fatalf("Content-Length = %q, want %d", got.Header().Get("Content-Length"), want.Body.Len())
			}

			// Compressed: same bytes once decoded.
			gz := pairRequest(t, mans, "gzip", "")
			body := decodeMaybeGzip(t, gz)
			if body != want.Body.String() {
				t.Fatalf("gzipped bytes differ once decoded\n got: %s\nwant: %s", body, want.Body.String())
			}
		})
	}
}

func decodeMaybeGzip(t *testing.T, rw *httptest.ResponseRecorder) string {
	t.Helper()
	if rw.Header().Get("Content-Encoding") != "gzip" {
		return rw.Body.String()
	}
	zr, err := gzip.NewReader(bytes.NewReader(rw.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip read: %v", err)
	}
	if n, _ := strconv.Atoi(rw.Header().Get("Content-Length")); n != rw.Body.Len() {
		t.Fatalf("Content-Length = %q, want %d compressed bytes", rw.Header().Get("Content-Length"), rw.Body.Len())
	}
	return string(out)
}

// A caller holding the current tag gets a 304 and no body, and the tag it
// was given still identifies the same representation.
func TestWriteSharedJSONPairCached_Revalidation(t *testing.T) {
	mans := catalogPairFixture()
	first := pairRequest(t, mans, "gzip", "")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag emitted")
	}
	if cc := first.Header().Get("Cache-Control"); cc != "private, no-cache" {
		t.Fatalf("Cache-Control = %q", cc)
	}

	second := pairRequest(t, mans, "gzip", etag)
	if second.Code != http.StatusNotModified {
		t.Fatalf("conditional request = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 carried %d body bytes", second.Body.Len())
	}
	if second.Header().Get("ETag") != etag {
		t.Fatalf("304 ETag = %q, want %q", second.Header().Get("ETag"), etag)
	}

	// A weak tag and a list both have to match, per RFC 9110's weak
	// comparison for If-None-Match.
	for _, header := range []string{`W/` + etag, `"other", ` + etag, "*"} {
		if rw := pairRequest(t, mans, "gzip", header); rw.Code != http.StatusNotModified {
			t.Fatalf("If-None-Match %s = %d, want 304", header, rw.Code)
		}
	}
}

// The tag is derived from the body, which is the whole reason the cache
// needs no invalidation: change the catalog and the tag moves, so a
// revalidation returns the new bytes rather than the held ones. This is
// also what keeps one tenant's catalog from being served as another's.
func TestWriteSharedJSONPairCached_TagFollowsContent(t *testing.T) {
	mans := catalogPairFixture()
	etag := pairRequest(t, mans, "gzip", "").Header().Get("ETag")

	changed := append([]core.Manifest(nil), mans...)
	changed[3].Label = "a different label entirely"
	after := pairRequest(t, changed, "gzip", etag)
	if after.Code != http.StatusOK {
		t.Fatalf("changed catalog with the old tag = %d, want 200", after.Code)
	}
	if after.Header().Get("ETag") == etag {
		t.Fatal("ETag did not move when the catalog changed")
	}
	// And the body really is the new catalog, not a cached older one.
	var body struct {
		Drops []core.Manifest `json:"drops"`
	}
	if err := json.Unmarshal([]byte(decodeMaybeGzip(t, after)), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.Drops[3].Label != "a different label entirely" {
		t.Fatalf("served drops[3].Label = %q, want the changed one", body.Drops[3].Label)
	}
}

// A deployment that opted out of compression must not get a compressed
// body from here either: with the opt-out set the streaming middleware is
// not installed, so this writer is the only thing that could encode.
func TestWriteSharedJSONPairCached_HonoursCompressionOptOut(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/drops", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rw := httptest.NewRecorder()
	writeSharedJSONPairCached(rw, req, "drops", "modules", catalogPairFixture(), false)
	if enc := rw.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("Content-Encoding = %q with compression disabled", enc)
	}
}

// A body under the compression threshold is sent as-is, and must not pick
// up a second Vary from the streaming middleware on the way out.
func TestWriteSharedJSONPairCached_SmallBodyNotCompressedOrDoubleVaried(t *testing.T) {
	small := []core.Manifest{{ID: "a", Label: "A"}}
	req := httptest.NewRequest("GET", "/api/v1/drops", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSharedJSONPairCached(w, r, "drops", "modules", small, true)
	})
	rw := httptest.NewRecorder()
	gzipResponses(true, inner).ServeHTTP(rw, req)

	if enc := rw.Header().Get("Content-Encoding"); enc != "" {
		t.Fatalf("Content-Encoding = %q for a %d-byte body", enc, rw.Body.Len())
	}
	if vary := rw.Header().Values("Vary"); len(vary) != 1 {
		t.Fatalf("Vary = %v, want exactly one Accept-Encoding", vary)
	}
}

// The pooled encode buffer must not survive into the cached compressed
// bytes: a leaked reference would show up as one goroutine's catalog
// appearing in another's response.
func TestWriteSharedJSONPairCached_Concurrent(t *testing.T) {
	mans := catalogPairFixture()
	want := httptest.NewRecorder()
	writeJSON(want, http.StatusOK, map[string]any{"drops": mans, "modules": mans})
	expect := want.Body.String()

	done := make(chan string, 16)
	for range 16 {
		go func() {
			req := httptest.NewRequest("GET", "/api/v1/drops", nil)
			req.Header.Set("Accept-Encoding", "gzip")
			rw := httptest.NewRecorder()
			writeSharedJSONPairCached(rw, req, "drops", "modules", mans, true)
			zr, err := gzip.NewReader(bytes.NewReader(rw.Body.Bytes()))
			if err != nil {
				done <- "gzip error: " + err.Error()
				return
			}
			out, err := io.ReadAll(zr)
			if err != nil {
				done <- "read error: " + err.Error()
				return
			}
			done <- string(out)
		}()
	}
	for range 16 {
		if got := <-done; got != expect {
			t.Fatalf("concurrent body differs:\n got: %s\nwant: %s", got, expect)
		}
	}
}

// Eviction must stay bounded and must never hand back another entry's
// bytes for a tag it no longer holds.
func TestGzipBodyCache_EvictsOldestAndStaysBounded(t *testing.T) {
	c := &gzipBodyCache{byTag: map[string][]byte{}}
	for i := range gzipBodyCacheMax * 3 {
		tag := strconv.Itoa(i)
		c.put(tag, []byte(tag))
		if got := c.get(tag); string(got) != tag {
			t.Fatalf("get(%s) = %q right after put", tag, got)
		}
	}
	if len(c.byTag) != gzipBodyCacheMax || len(c.order) != gzipBodyCacheMax {
		t.Fatalf("cache holds %d/%d entries, want %d", len(c.byTag), len(c.order), gzipBodyCacheMax)
	}
	// The oldest are gone, not merely unreachable.
	if got := c.get("0"); got != nil {
		t.Fatalf("evicted tag 0 still returns %q", got)
	}
}

// The full request path: the route emits a validator and answers a
// conditional request with a 304.
func TestListDrops_EmitsValidatorOverTheRoute(t *testing.T) {
	handler, token := benchGatewayT(t)
	req := httptest.NewRequest("GET", "/api/v1/drops", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept-Encoding", "gzip")
	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/drops = %d", rw.Code)
	}
	etag := rw.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag over the route")
	}
	if enc := rw.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", enc)
	}

	cond := httptest.NewRequest("GET", "/api/v1/drops", nil)
	cond.Header.Set("Authorization", "Bearer "+token)
	cond.Header.Set("Accept-Encoding", "gzip")
	cond.Header.Set("If-None-Match", etag)
	rw2 := httptest.NewRecorder()
	handler.ServeHTTP(rw2, cond)
	if rw2.Code != http.StatusNotModified {
		t.Fatalf("conditional GET /api/v1/drops = %d, want 304", rw2.Code)
	}
	if rw2.Body.Len() != 0 {
		t.Fatalf("304 carried %d bytes", rw2.Body.Len())
	}
}

// writeCachedJSON must put exactly the bytes writeJSON does on the wire,
// newline included, or a client diffing responses sees a change that isn't.
func TestWriteCachedJSON_SameBytesAsWriteJSON(t *testing.T) {
	body := map[string]any{
		"items": catalogPairFixture(),
		"page":  map[string]any{"next": nil, "size": 40, "total": 40},
	}
	want := httptest.NewRecorder()
	writeJSON(want, http.StatusOK, body)

	req := httptest.NewRequest("GET", "/api/v1/catalog/drops", nil)
	got := httptest.NewRecorder()
	writeCachedJSON(got, req, body, true)
	if got.Body.String() != want.Body.String() {
		t.Fatalf("bytes differ\n got: %q\nwant: %q",
			tail(got.Body.String()), tail(want.Body.String()))
	}

	req.Header.Set("Accept-Encoding", "gzip")
	gz := httptest.NewRecorder()
	writeCachedJSON(gz, req, body, true)
	if decoded := decodeMaybeGzip(t, gz); decoded != want.Body.String() {
		t.Fatalf("gzipped bytes differ once decoded\n got: %q\nwant: %q",
			tail(decoded), tail(want.Body.String()))
	}
	if etag := gz.Header().Get("ETag"); etag == "" {
		t.Fatal("no ETag")
	} else if cond := httptest.NewRecorder(); true {
		req.Header.Set("If-None-Match", etag)
		writeCachedJSON(cond, req, body, true)
		if cond.Code != http.StatusNotModified {
			t.Fatalf("conditional = %d, want 304", cond.Code)
		}
	}
}

func tail(s string) string {
	if len(s) <= 120 {
		return s
	}
	return "..." + s[len(s)-120:]
}
