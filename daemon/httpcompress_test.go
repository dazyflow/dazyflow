// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// serveCompressed runs one handler through the compression middleware.
func serveCompressed(t *testing.T, acceptEncoding string, h http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", "/x", nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rw := httptest.NewRecorder()
	gzipResponses(true, h).ServeHTTP(rw, req)
	return rw
}

func bigJSON() string { return `{"k":"` + strings.Repeat("compress me ", 400) + `"}` }

func TestGzip_CompressesLargeJSON(t *testing.T) {
	body := bigJSON()
	rw := serveCompressed(t, "gzip", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
	if got := rw.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if rw.Body.Len() >= len(body) {
		t.Fatalf("body not smaller: %d >= %d", rw.Body.Len(), len(body))
	}
	zr, err := gzip.NewReader(rw.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != body {
		t.Fatal("round trip changed the body")
	}
}

// Vary must be present so a shared cache can't hand a gzipped body to a
// client that never asked for one. It is Added, not Set, because the CORS
// layer already put Origin there.
func TestGzip_VaryAppended(t *testing.T) {
	rw := serveCompressed(t, "gzip", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, bigJSON())
	})
	vary := rw.Header().Values("Vary")
	if !slices.Contains(vary, "Accept-Encoding") || !slices.Contains(vary, "Origin") {
		t.Fatalf("Vary = %v, want both Origin and Accept-Encoding", vary)
	}
}

func TestGzip_SkippedWithoutAcceptEncoding(t *testing.T) {
	body := bigJSON()
	rw := serveCompressed(t, "", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
	if rw.Header().Get("Content-Encoding") != "" {
		t.Fatal("compressed a client that never asked")
	}
	if rw.Body.String() != body {
		t.Fatal("body altered")
	}
}

// The load-bearing one: an SSE stream must stay uncompressed and must
// still flush frame by frame. Compressing it would hold frames in the
// window and break every live view in the product.
func TestGzip_LeavesEventStreamAlone(t *testing.T) {
	rw := serveCompressed(t, "gzip", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("handler lost http.Flusher through the compression wrapper")
			return
		}
		for range 3 {
			_, _ = io.WriteString(w, "data: tick\n\n")
			f.Flush()
		}
	})
	if got := rw.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("SSE was compressed (Content-Encoding=%q)", got)
	}
	if !rw.Flushed {
		t.Fatal("SSE frames never flushed")
	}
	if strings.Count(rw.Body.String(), "data: tick") != 3 {
		t.Fatalf("body = %q", rw.Body.String())
	}
}

func TestGzip_SkipsSmallDeclaredBodies(t *testing.T) {
	rw := serveCompressed(t, "gzip", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "9")
		_, _ = io.WriteString(w, `{"ok":11}`)
	})
	if rw.Header().Get("Content-Encoding") != "" {
		t.Fatal("compressed a body below the threshold")
	}
	if rw.Body.String() != `{"ok":11}` {
		t.Fatalf("body = %q", rw.Body.String())
	}
}

func TestGzip_SkipsBinaryAndPreEncoded(t *testing.T) {
	t.Run("binary", func(t *testing.T) {
		rw := serveCompressed(t, "gzip", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(make([]byte, 4096))
		})
		if rw.Header().Get("Content-Encoding") != "" {
			t.Fatal("compressed an already-compressed media type")
		}
	})
	t.Run("pre-encoded", func(t *testing.T) {
		rw := serveCompressed(t, "gzip", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "br")
			_, _ = io.WriteString(w, bigJSON())
		})
		if got := rw.Header().Get("Content-Encoding"); got != "br" {
			t.Fatalf("Content-Encoding = %q, want the handler's own br", got)
		}
	})
}

func TestGzip_NoBodyStatuses(t *testing.T) {
	rw := serveCompressed(t, "gzip", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNoContent)
	})
	if rw.Header().Get("Content-Encoding") != "" {
		t.Fatal("204 must carry no encoding")
	}
	if rw.Code != http.StatusNoContent {
		t.Fatalf("code = %d", rw.Code)
	}
}

// Accept-Encoding is a list with optional q-params; "x-gzip, gzip;q=0.9"
// and "gzipzz" must be told apart.
func TestGzip_AcceptEncodingParsing(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   bool
	}{
		{"gzip", true},
		{"gzip;q=0.9", true},
		{"br, gzip", true},
		{" GZIP ", true},
		{"deflate", false},
		{"gzipzz", false},
		{"", false},
	} {
		req := httptest.NewRequest("GET", "/x", nil)
		if tc.header != "" {
			req.Header.Set("Accept-Encoding", tc.header)
		}
		if got := acceptsGzip(req); got != tc.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

// The operator escape hatch: with compression disabled the middleware
// must add no wrapper at all, so a proxy that already encodes is not
// compressing bytes twice.
func TestGzip_DisabledPassesThrough(t *testing.T) {
	body := bigJSON()
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rw := httptest.NewRecorder()
	gzipResponses(false, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})).ServeHTTP(rw, req)

	if rw.Header().Get("Content-Encoding") != "" {
		t.Fatal("compressed while disabled")
	}
	if rw.Body.String() != body {
		t.Fatal("body altered while disabled")
	}
}

// Over a real socket, not a recorder: chunked framing, the deleted
// Content-Length, and a real client's decoding all have to agree. A
// recorder cannot show any of that.
func TestGzip_OverRealTransport(t *testing.T) {
	body := bigJSON()
	srv := httptest.NewServer(gzipResponses(true, http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, body)
		})))
	defer srv.Close()

	t.Run("explicit gzip is compressed on the wire", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		// Setting it by hand turns OFF the transport's transparent
		// decompression, so what arrives is what went over the wire.
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding = %q, want gzip", got)
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if len(raw) >= len(body) {
			t.Fatalf("wire bytes %d not smaller than %d", len(raw), len(body))
		}
		zr, err := gzip.NewReader(strings.NewReader(string(raw)))
		if err != nil {
			t.Fatalf("gzip.NewReader: %v", err)
		}
		got, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("inflate: %v", err)
		}
		if string(got) != body {
			t.Fatal("inflated body differs from what the handler wrote")
		}
		t.Logf("wire: %d bytes for a %d byte body (%.1fx)",
			len(raw), len(body), float64(len(body))/float64(len(raw)))
	})

	t.Run("default client round-trips transparently", func(t *testing.T) {
		resp, err := srv.Client().Get(srv.URL)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		defer resp.Body.Close()
		got, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != body {
			t.Fatalf("transparent decode gave %d bytes, want %d", len(got), len(body))
		}
	})
}
