// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const gzipMinSize = 1400

// gzipLevel trades ratio for CPU. BestSpeed is the right end of that curve
// for a server: on the ~1 MB drop catalog it gives up 42KB of ratio (267KB
// against default compression's 225KB) to halve the compression CPU
// (+10.6ms against +22ms on a request whose body otherwise costs 5ms).
// Measured in tests/perf/README.md, "The response path".
const gzipLevel = gzip.BestSpeed

var gzipWriterPool = sync.Pool{
	New: func() any {
		zw, _ := gzip.NewWriterLevel(nil, gzipLevel)
		return zw
	},
}

// compressibleType reports whether a Content-Type is worth gzipping.
// text/event-stream is excluded deliberately: an SSE stream is a sequence
// of small frames a browser must see immediately, and buffering one into
// a compression window defeats the point of the endpoint.
func compressibleType(ct string) bool {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ct == "text/event-stream" {
		return false
	}
	switch {
	case strings.HasPrefix(ct, "text/"),
		ct == "application/json",
		ct == "application/xml",
		ct == "application/javascript",
		ct == "image/svg+xml",
		strings.HasSuffix(ct, "+json"),
		strings.HasSuffix(ct, "+xml"):
		return true
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	for enc := range strings.SplitSeq(r.Header.Get("Accept-Encoding"), ",") {
		name, _, _ := strings.Cut(enc, ";")
		if strings.EqualFold(strings.TrimSpace(name), "gzip") {
			return true
		}
	}
	return false
}

// gzipResponses compresses eligible response bodies for clients that
// asked for it. The decision is made at WriteHeader time from the
// Content-Type the handler chose — never by buffering — so a stream stays
// a stream, exactly as jsonErrors decides its rewrite.
func gzipResponses(enabled bool, next http.Handler) http.Handler {
	if !enabled {
		return next
	}
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if !acceptsGzip(r) {
			next.ServeHTTP(rw, r)
			return
		}
		gw := &gzipResponseWriter{ResponseWriter: rw}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	zw   *gzip.Writer // non-nil once compressing
	done bool         // header decision made
}

func (w *gzipResponseWriter) WriteHeader(status int) {
	if w.done {
		return
	}
	w.done = true
	h := w.Header()
	noBody := status < 200 || status == http.StatusNoContent || status == http.StatusNotModified
	if noBody || h.Get("Content-Encoding") != "" || !compressibleType(h.Get("Content-Type")) {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	// Vary goes on every compressible response, including the ones left
	// uncompressed below: a cache keyed without it would serve a gzipped
	// body to a client that never asked for one. A handler that already
	// declared it (the response cache does) must not get it twice.
	addVaryAcceptEncoding(h)
	if n, err := strconv.Atoi(h.Get("Content-Length")); err == nil && n < gzipMinSize {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	h.Set("Content-Encoding", "gzip")
	// The compressed length is not known yet, and a stale one is worse
	// than none: Go will close the connection to delimit the body.
	h.Del("Content-Length")
	w.zw = gzipWriterPool.Get().(*gzip.Writer)
	w.zw.Reset(w.ResponseWriter)
	w.ResponseWriter.WriteHeader(status)
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	if !w.done {
		w.WriteHeader(http.StatusOK)
	}
	if w.zw != nil {
		return w.zw.Write(b)
	}
	return w.ResponseWriter.Write(b)
}

func (w *gzipResponseWriter) Flush() {
	if w.zw != nil {
		_ = w.zw.Flush()
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *gzipResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *gzipResponseWriter) close() {
	if w.zw == nil {
		return
	}
	_ = w.zw.Close()
	gzipWriterPool.Put(w.zw)
	w.zw = nil
}
