package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// ApprovalDecision is the input from the human approver, accepted by
// Service.Approve and forwarded to the resumed node-record as Result
// output ports.
type ApprovalDecision struct {
	Decision string // "approve" | "reject"
	Approver string
	Comment  string
}

// HMACApprovalSigner mints deterministic per-(graphRunID, nodeID) URLs
// signed with HMAC-SHA256 over a shared secret. Deterministic so that a
// retried await_approval Execute (after a lease expiry) re-emits the
// same URL: external systems that already received the URL by email
// don't have to be re-notified.
//
// BaseURL should be the externally-visible address of the approval
// listener, e.g. "https://hzd.acme.com". Token validation lives in
// ApprovalListener.
type HMACApprovalSigner struct {
	BaseURL string
	Secret  []byte
}

// SignApprovalURL builds the absolute URL the approver hits. Format:
//
//	<base>/approve/<graphRunID>/<nodeID>?token=<hex>
func (s *HMACApprovalSigner) SignApprovalURL(graphRunID, nodeID string) string {
	token := s.computeToken(graphRunID, nodeID)
	return fmt.Sprintf("%s/approve/%s/%s?token=%s", s.BaseURL, graphRunID, nodeID, token)
}

func (s *HMACApprovalSigner) computeToken(graphRunID, nodeID string) string {
	m := hmac.New(sha256.New, s.Secret)
	m.Write([]byte(graphRunID))
	m.Write([]byte(":"))
	m.Write([]byte(nodeID))
	return hex.EncodeToString(m.Sum(nil))
}

// verifyToken does constant-time comparison so a malicious approver
// can't time-side-channel the expected token character by character.
func (s *HMACApprovalSigner) verifyToken(graphRunID, nodeID, provided string) bool {
	expected := s.computeToken(graphRunID, nodeID)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

// Approve is the resume path: a human (via ApprovalListener) signals
// their decision and the daemon transitions the awaiting node-record to
// Succeeded with the decision recorded in the Result. Downstream nodes
// then proceed exactly as if a regular node had emitted on the
// approved/rejected port.
//
// Errors:
//   - ErrNotFound if the node-record doesn't exist
//   - ErrConflict if the record isn't actually awaiting (already
//     resumed, never paused, or hit by two concurrent approves)
func (s *Service) Approve(
	ctx context.Context,
	graphRunID, nodeID string,
	decision ApprovalDecision,
) error {
	if decision.Decision != "approve" && decision.Decision != "reject" {
		return fmt.Errorf("decision must be approve or reject, got %q", decision.Decision)
	}
	nodeRecID := NodeJobID(graphRunID, nodeID)
	rec, err := s.Jobs.Get(ctx, nodeRecID)
	if err != nil {
		return fmt.Errorf("get node record: %w", err)
	}
	if rec.Status != core.JobStatusAwaiting {
		return fmt.Errorf("node %s is %s, not awaiting", nodeID, rec.Status)
	}

	// Build the resume Result. Preserve any pending output the awaiting
	// Execute already wrote (notably the context passthrough) so
	// downstream nodes wired to that port still see their data.
	output := map[string]core.Ref{
		"decision": {MIME: "text/plain", Inline: decision.Decision},
		"approver": {MIME: "text/plain", Inline: decision.Approver},
		"comment":  {MIME: "text/plain", Inline: decision.Comment},
	}
	if decision.Decision == "approve" {
		output["approved"] = core.Ref{MIME: "application/x-control"}
	} else {
		output["rejected"] = core.Ref{MIME: "application/x-control"}
	}
	if rec.Result != nil {
		if ctxRef, ok := rec.Result.Output["context"]; ok {
			output["context"] = ctxRef
		}
	}

	resumeResult := &core.Result{
		JobID:  nodeRecID,
		Status: core.StatusOK,
		Output: output,
	}
	if err := s.Jobs.Complete(ctx, nodeRecID, core.JobStatusSucceeded, resumeResult); err != nil {
		return fmt.Errorf("complete: %w", err)
	}

	// Advance the graph: load the graph payload from the graph-record,
	// then run the shared dispatcher.
	graphRec, err := s.Jobs.Get(ctx, graphRunID)
	if err != nil {
		return fmt.Errorf("get graph record: %w", err)
	}
	if len(graphRec.GraphPayload) == 0 {
		return fmt.Errorf("graph record has no payload")
	}
	var g core.Graph
	if err := json.Unmarshal(graphRec.GraphPayload, &g); err != nil {
		return fmt.Errorf("unmarshal graph: %w", err)
	}
	disp := NewDispatcher(s.Jobs, s.bus(), s.Engine, log.New(log.Writer(), "approve: ", log.LstdFlags))
	disp.AdvanceAfterCompletion(ctx, g, graphRunID, nodeID, core.JobStatusSucceeded, nil)
	return nil
}

// ApprovalListener is the HTTP front for Service.Approve. The endpoint
// is intentionally thin: token check, parameter parse, call Approve,
// return JSON. Network operators put it behind their normal ingress
// (TLS, optional VPN, audit logging) — the listener itself doesn't
// duplicate that infrastructure.
type ApprovalListener struct {
	svc    *Service
	signer *HMACApprovalSigner
	logger *log.Logger
}

func NewApprovalListener(svc *Service, signer *HMACApprovalSigner) *ApprovalListener {
	return &ApprovalListener{
		svc:    svc,
		signer: signer,
		logger: log.New(log.Writer(), "approve: ", log.LstdFlags),
	}
}

func (a *ApprovalListener) handle(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/approve/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(rw, "expected /approve/<graphRunID>/<nodeID>", http.StatusBadRequest)
		return
	}
	graphRunID, nodeID := parts[0], parts[1]
	token := r.URL.Query().Get("token")
	if token == "" || !a.signer.verifyToken(graphRunID, nodeID, token) {
		http.Error(rw, "invalid token", http.StatusUnauthorized)
		return
	}
	decision := r.URL.Query().Get("decision")
	if decision == "" {
		decision = "approve"
	}
	approver := r.URL.Query().Get("approver")
	comment := r.URL.Query().Get("comment")

	if err := a.svc.Approve(r.Context(), graphRunID, nodeID, ApprovalDecision{
		Decision: decision,
		Approver: approver,
		Comment:  comment,
	}); err != nil {
		a.logger.Printf("approve %s/%s: %v", graphRunID, nodeID, err)
		// Distinguish "wrong state" (409) from "no such record" (404) so
		// approvers can distinguish a duplicate click from a bad link.
		if strings.Contains(err.Error(), "not awaiting") {
			http.Error(rw, err.Error(), http.StatusConflict)
			return
		}
		if strings.Contains(err.Error(), "not found") {
			http.Error(rw, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	a.logger.Printf("resumed %s/%s decision=%s approver=%s", graphRunID, nodeID, decision, approver)
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(rw).Encode(map[string]string{
		"status":   "resumed",
		"decision": decision,
	})
}

// ServeApprovalForTest exposes the listener's handler without binding a
// port — analogous to ServeWebhookForTest. Production code uses Serve.
func ServeApprovalForTest(a *ApprovalListener, rw http.ResponseWriter, r *http.Request) {
	a.handle(rw, r)
}
