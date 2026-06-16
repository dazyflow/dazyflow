package daemon

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// ErrorEnvelope is the structured error shape every spec-aligned
// endpoint returns on a 4xx/5xx response. The shape matches the
// `ErrorEnvelope` schema in daemon/openapi.yaml — wire format is:
//
//	{
//	  "error": {
//	    "code":    "drop_not_found",
//	    "message": "no such drop: foo",
//	    "details": [ {"field": "params.channel", "issue": "required"} ],
//	    "doc":     "/api/v1/openapi.json#/.../SlackSendMessage"
//	  }
//	}
//
// The legacy {"error": "<string>"} shape is still emitted by
// writeJSONError on routes that haven't been migrated yet (most of
// the gateway today). The web client's parser accepts both.
//
// Code is a stable snake_case enum for machine-readable branching.
// Message is the human/LLM-readable explanation — keep it
// actionable. Details is optional and structured (per-field
// validation failures). Doc is an optional deep link into the
// OpenAPI spec describing the expected shape — invaluable for an
// LLM that hit a 4xx and needs to read what it should have sent.
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

// writeAPIError emits the structured envelope. Every error response on
// the API goes through here — either directly (handlers with a specific
// machine code) or via writeJSONError (which derives the code from the
// HTTP status). Pass an empty `details` slice (or no extra args) when you
// don't have per-field validation info.
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

// codeForStatus maps an HTTP status to the stable snake_case error code
// used when a caller only supplied a status + message. Keeps the envelope
// machine-readable even for the generic legacy error paths.
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

// jsonErrors wraps a handler so the Go ServeMux's built-in plain-text
// responses for an unmatched route (404) and a method mismatch (405) come
// back as the same JSON ErrorEnvelope as every other error. It decides at
// WriteHeader time — never buffers — so long-lived SSE streams are
// untouched. The discriminator is Content-Type: the mux's defaults set
// text/plain, while our own handlers always set application/json (so a
// handler-produced 404/405 passes through unchanged).
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
	isMuxDefault := (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) &&
		!strings.HasPrefix(w.Header().Get("Content-Type"), "application/json")
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

// Flush propagates to the underlying writer when it supports it, so SSE
// streams keep flushing through the wrapper.
func (w *jsonErrorWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
