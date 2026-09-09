// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/dazyflow/dazyflow/core"
)

// ErrorEnvelope is the structured error every spec-aligned endpoint returns on
// a 4xx/5xx; the shape matches the `ErrorEnvelope` schema in openapi.yaml.
// Routes that have not been migrated still emit the legacy {"error":"<string>"}
// via writeJSONError, and the web client's parser accepts both.
//
// Code is the stable snake_case discriminator; Doc deep-links the spec for the
// expected shape, which is what an LLM needs after a 4xx.
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string        `json:"code"`
	Message string        `json:"message"`
	Details []ErrorDetail `json:"details,omitempty"`
	Doc     string        `json:"doc,omitempty"`
}

type ErrorDetail struct {
	Field string `json:"field,omitempty"`
	Issue string `json:"issue,omitempty"`
}

func writeAPIError(rw http.ResponseWriter, status int, code, message string, details ...ErrorDetail) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	env := ErrorEnvelope{Error: ErrorBody{
		Code:    code,
		Message: message,
		Details: details,
	}}
	// Encode failures here mean the client connection broke mid-write, so the
	// caller already got a partial/empty body. Log it so a truncated error
	// response leaves a trace instead of vanishing silently.
	if err := json.NewEncoder(rw).Encode(env); err != nil {
		log.Printf("writeAPIError: encode envelope (status=%d code=%s): %v", status, code, err)
	}
}

func codeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusMethodNotAllowed:
		return "method_not_allowed"
	case http.StatusConflict:
		return "conflict"
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusInsufficientStorage:
		// 507 is user-actionable (their storage quota), NOT an "our side"
		// outage — give it a distinct code so the web UI can show friendly,
		// correct guidance instead of the generic 5xx "try again" message.
		return "storage_full"
	case http.StatusNotImplemented:
		return "not_implemented"
	case http.StatusServiceUnavailable:
		return "unavailable"
	default:
		if status >= 500 {
			return "internal_error"
		}
		return "error"
	}
}

func decodeRequestJSON[T any](rw http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "decode_failed", "decode body: "+err.Error())
		return v, false
	}
	return v, true
}

func decodeRequestJSONOptional[T any](rw http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	if r.Body == nil {
		return v, true
	}
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil && !errors.Is(err, io.EOF) {
		writeAPIError(rw, http.StatusBadRequest, "decode_failed", "decode body: "+err.Error())
		return v, false
	}
	return v, true
}

func requireOrgAdmin(rw http.ResponseWriter, p core.Principal) bool {
	if !core.CanAdminOrg(p) {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "organization:admin required")
		return false
	}
	return true
}

// jsonErrors rewrites the ServeMux's plain-text 404/405 as the same JSON
// envelope as every other error. It decides at WriteHeader time and never
// buffers, so SSE streams are untouched.
//
// Content-Type is the discriminator, and it names what gets rewritten:
// text/plain (what http.Error and the mux set) or nothing at all. Anything else
// is a handler that chose its own representation — text/html in particular is
// the public hosted form, the one URL an owner gives their customers.
func jsonErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&jsonErrorWriter{ResponseWriter: rw}, r)
	})
}

type jsonErrorWriter struct {
	http.ResponseWriter
	swallow bool // mux wrote a default plain-text error; drop its body
	done    bool // header already processed
}

func (w *jsonErrorWriter) WriteHeader(status int) {
	if w.done {
		return
	}
	w.done = true
	ct := w.Header().Get("Content-Type")
	isMuxDefault := (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) &&
		(ct == "" || strings.HasPrefix(ct, "text/plain"))
	if isMuxDefault {
		w.swallow = true
		w.Header().Set("Content-Type", "application/json")
		w.ResponseWriter.WriteHeader(status)
		if err := json.NewEncoder(w.ResponseWriter).Encode(ErrorEnvelope{Error: ErrorBody{
			Code:    codeForStatus(status),
			Message: http.StatusText(status),
		}}); err != nil {
			log.Printf("jsonErrors: encode default %d envelope: %v", status, err)
		}
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *jsonErrorWriter) Write(b []byte) (int, error) {
	if !w.done {
		w.WriteHeader(http.StatusOK)
	}
	if w.swallow {
		return len(b), nil // pretend success; body already written as JSON
	}
	return w.ResponseWriter.Write(b)
}

func (w *jsonErrorWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *jsonErrorWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
