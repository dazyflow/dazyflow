// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// Content-addressed caching for the API's few large, repeatable bodies.
//
// The drop catalog is ~1 MB of JSON and 71% of its request is spent
// compressing bytes that are, almost always, the ones compressed a moment
// ago. The validator here is derived from the body itself, and that is what
// makes it safe with no invalidation to wire up: the catalog varies by
// tenant, by platform drop switches and by whichever models a tenant's
// credential can currently call, and none of those have to be tracked,
// because different bytes give a different tag. Two tenants share an entry
// only when their catalogs are byte-identical, so there is no way to serve
// one org's private runner drops to another.

import (
	"bytes"
	"compress/gzip"
	"hash/crc32"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// crcTable is Castagnoli, the polynomial with SSE4.2 hardware support:
// 12.8 GB/s measured here against 207 MB/s for SHA-256. Fingerprinting the
// catalog costs 0.08ms where compressing it costs 10.7ms, which is the only
// reason this trade works.
var crcTable = crc32.MakeTable(crc32.Castagnoli)

// etagOfParts fingerprints a body given as its write parts, without
// joining them. CRC plus length: the CRC alone would already put two
// versions of one catalog colliding at odds of 1 in 4 billion, and the
// length covers the append-shaped changes a CRC is weakest against.
func etagOfParts(parts [][]byte) (etag string, total int) {
	var crc uint32
	for _, p := range parts {
		crc = crc32.Update(crc, crcTable, p)
		total += len(p)
	}
	return `"` + strconv.FormatUint(uint64(crc), 16) + "-" + strconv.Itoa(total) + `"`, total
}

// etagMatches implements the If-None-Match comparison: a comma-separated
// list, "*" for any representation, and weak tags compared against their
// strong form, which is the weak comparison RFC 9110 specifies for this
// header.
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	if strings.TrimSpace(header) == "*" {
		return true
	}
	for cand := range strings.SplitSeq(header, ",") {
		if strings.TrimPrefix(strings.TrimSpace(cand), "W/") == etag {
			return true
		}
	}
	return false
}

// gzipBodyCacheMax bounds the cache by entries. The key is content, so the
// population is the number of distinct catalogs in play — a handful of
// tenant and flag variants — not the number of tenants or of requests. At
// ~267 KB per compressed catalog, eight entries is ~2 MB, the right order
// against the 512Mi pod limit in deploy/k8s/. Overflowing it costs the
// compression this avoids, nothing worse.
const gzipBodyCacheMax = 8

// gzipBodyCache holds compressed bodies keyed by their content
// fingerprint. Eviction is oldest-first rather than least-recently-used:
// the entries are interchangeable in cost and there are eight of them, so
// the bookkeeping LRU wants would buy nothing.
type gzipBodyCache struct {
	mu    sync.Mutex
	byTag map[string][]byte
	order []string
}

var sharedGzipBodies = &gzipBodyCache{byTag: map[string][]byte{}}

func (c *gzipBodyCache) get(tag string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byTag[tag]
}

func (c *gzipBodyCache) put(tag string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, dup := c.byTag[tag]; dup {
		return
	}
	if len(c.order) >= gzipBodyCacheMax {
		delete(c.byTag, c.order[0])
		// Shift in place: re-slicing forward would walk the backing array
		// off the end and reallocate on every eviction.
		c.order = append(c.order[:0], c.order[1:]...)
	}
	c.byTag[tag] = body
	c.order = append(c.order, tag)
}

// gzipJoin compresses the body parts into one buffer to be cached and
// written whole. Unlike the streaming middleware this knows the finished
// length, so the response carries a Content-Length instead of being
// chunked. Returns nil if compression fails, which leaves the caller on
// the uncompressed path.
func gzipJoin(parts [][]byte, total int) []byte {
	// Size the buffer at the ratio the catalog actually achieves (~4:1) so
	// it does not climb the doubling ladder to reach 267 KB.
	buf := bytes.NewBuffer(make([]byte, 0, total/4+1024))
	zw := gzipWriterPool.Get().(*gzip.Writer)
	defer gzipWriterPool.Put(zw)
	zw.Reset(buf)
	for _, p := range parts {
		if _, err := zw.Write(p); err != nil {
			return nil
		}
	}
	if err := zw.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}

// addVaryAcceptEncoding records that the body depends on the encoding the
// client asked for, without repeating a value already there — the response
// cache below and the streaming middleware can both reach the same header.
func addVaryAcceptEncoding(h http.Header) {
	for _, v := range h.Values("Vary") {
		for field := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(field), "Accept-Encoding") {
				return
			}
		}
	}
	h.Add("Vary", "Accept-Encoding")
}

// writeCachedParts serves a body already framed into its write parts,
// giving it a content-derived ETag, a compressed form held across requests,
// and a 304 when the caller still holds the same bytes. The caller sets
// Content-Type first. allowGzip is false when the deployment opted out of
// compression, in which case the streaming middleware is not installed
// either and this must not compress on its own.
func writeCachedParts(rw http.ResponseWriter, r *http.Request, parts [][]byte, allowGzip bool) {
	etag, total := etagOfParts(parts)
	h := rw.Header()
	h.Set("ETag", etag)
	// Stored, but revalidated every time. The catalog moves when a runner
	// registers, an admin flips a drop switch, or a credential's model list
	// changes, so no freshness lifetime is defensible — but the tag is the
	// body's own fingerprint, so a revalidation that matches is proof the
	// held copy is current. "private" keeps it out of shared caches; it
	// needs no Vary on Cookie for the same content-addressed reason, since
	// a different user's catalog has a different tag and revalidates into a
	// 200 carrying their own bytes.
	h.Set("Cache-Control", "private, no-cache")
	addVaryAcceptEncoding(h)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		rw.WriteHeader(http.StatusNotModified)
		return
	}
	if allowGzip && total >= gzipMinSize && acceptsGzip(r) {
		gz := sharedGzipBodies.get(etag)
		if gz == nil {
			if gz = gzipJoin(parts, total); gz != nil {
				sharedGzipBodies.put(etag, gz)
			}
		}
		if gz != nil {
			// Setting the encoding here is also what tells the streaming
			// middleware to pass this response through untouched.
			h.Set("Content-Encoding", "gzip")
			h.Set("Content-Length", strconv.Itoa(len(gz)))
			rw.WriteHeader(http.StatusOK)
			_, _ = rw.Write(gz)
			return
		}
	}
	h.Set("Content-Length", strconv.Itoa(total))
	rw.WriteHeader(http.StatusOK)
	for _, p := range parts {
		if _, err := rw.Write(p); err != nil {
			return
		}
	}
}
