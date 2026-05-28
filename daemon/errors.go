package daemon

import (
	"encoding/json"
	"net/http"
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

// writeAPIError emits the structured envelope. Catalog, /me, and
// future spec-aligned handlers use this; legacy handlers stick with
// writeJSONError until the gateway-wide migration. Pass an empty
// `details` slice (or no extra args) when you don't have per-field
// validation info.
func writeAPIError(rw http.ResponseWriter, status int, code, message string, details ...ErrorDetail) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	env := ErrorEnvelope{Error: ErrorBody{
		Code:    code,
		Message: message,
		Details: details,
	}}
	_ = json.NewEncoder(rw).Encode(env)
}
