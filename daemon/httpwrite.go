// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// The gateway's response writers. Every handler in the package funnels
// through these, so status/content-type handling stays in one place.

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
)

func writeJSON(rw http.ResponseWriter, status int, body any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(body)
}

// writeXML is the XML analogue of writeJSON — it emits the XML declaration
// followed by the marshaled body. Used by endpoints that content-negotiate
// an XML representation (see wantsXML); the body's xml struct tags decide the
// element names, mirroring the JSON tags.
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
