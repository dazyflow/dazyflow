// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sync"
)

func writeJSON(rw http.ResponseWriter, status int, body any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(body)
}

func writeXML(rw http.ResponseWriter, status int, body any) {
	rw.Header().Set("Content-Type", "application/xml; charset=utf-8")
	rw.WriteHeader(status)
	_, _ = io.WriteString(rw, xml.Header)
	_ = xml.NewEncoder(rw).Encode(body)
}

// writeJSONError emits the structured ErrorEnvelope with a code derived
// from the HTTP status. It exists so the ~260 legacy call sites that only
// have (status, message) still produce the SAME wire shape as
// writeAPIError — one error envelope across the whole API. Handlers that
// have a more specific machine code should call writeAPIError directly.
func writeJSONError(rw http.ResponseWriter, status int, msg string) {
	writeAPIError(rw, status, codeForStatus(status), msg)
}

// writeSSE writes a single Server-Sent Events frame: `event: <name>\n
// data: <json>\n\n`. The browser's EventSource parser dispatches on
// the event name without parsing the payload twice. Used by the job
// events stream.
func writeSSE(rw http.ResponseWriter, event string, payload any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", event, b)
}

var jsonBufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// jsonBufMaxKeep is the largest buffer worth holding between requests. A
// pooled buffer is per-P, so a handful of oversized ones would be pinned
// for the process's life; the catalog fits well under this.
const jsonBufMaxKeep = 1 << 21 // 2 MiB

func encodeJSONPooled(v any, fn func([]byte)) error {
	buf := jsonBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer func() {
		if buf.Cap() <= jsonBufMaxKeep {
			jsonBufPool.Put(buf)
		}
	}()
	if err := json.NewEncoder(buf).Encode(v); err != nil {
		return err
	}
	b := buf.Bytes()
	fn(bytes.TrimSuffix(b, []byte("\n")))
	return nil
}

func writeCachedJSON(rw http.ResponseWriter, r *http.Request, v any, allowGzip bool) {
	err := encodeJSONPooled(v, func(value []byte) {
		rw.Header().Set("Content-Type", "application/json")
		writeCachedParts(rw, r, [][]byte{value, []byte("\n")}, allowGzip)
	})
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
	}
}

// writeSharedJSONPairCached writes {"<a>":<v>,"<b>":<v>} for one value
// encoded ONCE, through the response cache. It exists for the drop
// catalog, which is ~1 MB and is served under both its canonical key and
// its legacy alias: encoding it twice (what handing one slice to a map
// does), or re-parsing it once through a json.RawMessage, both cost more
// than the whole rest of the request — measured. Keys are compile-time
// constants at the call site, so they need no escaping.
func writeSharedJSONPairCached(rw http.ResponseWriter, r *http.Request, a, b string, v any, allowGzip bool) {
	err := encodeJSONPooled(v, func(value []byte) {
		rw.Header().Set("Content-Type", "application/json")
		writeCachedParts(rw, r, [][]byte{
			[]byte(`{"` + a + `":`), value,
			[]byte(`,"` + b + `":`), value,
			[]byte("}\n"),
		}, allowGzip)
	})
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
	}
}
