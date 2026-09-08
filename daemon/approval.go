// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/maillang"
)

const approvalTokenTTL = 14 * 24 * time.Hour

// Quantizes the signed expiry, so re-signing the same pause yields the SAME URL
// — otherwise every notification carried a different link for one decision.
const approvalTokenBucket = time.Hour

type ApprovalDecision struct {
	Decision string // "approve" | "reject"
	Approver string
	Comment  string
}

// Per-(run, node) and HMAC-signed, so a job id alone is not a capability.
type HMACApprovalSigner struct {
	BaseURL string
	Secret  []byte

	now func() time.Time
}

func (s *HMACApprovalSigner) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// The absolute URL an approver hits; possession of it is the capability.
func (s *HMACApprovalSigner) SignApprovalURL(graphRunID, nodeID string) string {
	exp := s.clock().Truncate(approvalTokenBucket).Add(approvalTokenTTL).Unix()
	token := s.computeToken(graphRunID, nodeID, exp)
	return fmt.Sprintf("%s/approve/%s/%s?exp=%d&token=%s",
		s.BaseURL, graphRunID, nodeID, exp, token)
}

func (s *HMACApprovalSigner) computeToken(graphRunID, nodeID string, exp int64) string {
	m := hmac.New(sha256.New, s.Secret)
	m.Write([]byte(graphRunID))
	m.Write([]byte(":"))
	m.Write([]byte(nodeID))
	m.Write([]byte(":"))
	m.Write([]byte(strconv.FormatInt(exp, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

// Constant-time, so a holder cannot brute-force a signature byte by byte.
func (s *HMACApprovalSigner) verifyToken(graphRunID, nodeID string, exp int64, provided string) bool {
	expected := s.computeToken(graphRunID, nodeID, exp)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) != 1 {
		return false
	}
	return s.clock().Unix() <= exp
}

var errBadApprovalDecision = errors.New("invalid approval decision")

// The resume path for a parked node.
func (s *Service) Approve(
	ctx context.Context,
	graphRunID, nodeID string,
	decision ApprovalDecision,
) error {
	if decision.Decision != "approve" && decision.Decision != "reject" {
		return fmt.Errorf("%w: decision must be approve or reject, got %q",
			errBadApprovalDecision, decision.Decision)
	}
	nodeRecID := NodeJobID(graphRunID, nodeID)
	rec, err := s.Jobs.Get(ctx, nodeRecID)
	if err != nil {
		return fmt.Errorf("get node record: %w", err)
	}
	// Wrapped as a sentinel, so callers classify by errors.Is, not by message.
	if rec.Status != core.JobStatusAwaiting {
		return fmt.Errorf("node %s is %s, not awaiting: %w", nodeID, rec.Status, core.ErrConflict)
	}

	output := map[string]core.Ref{}
	if rec.Result != nil {
		for port, ref := range rec.Result.Output {
			output[port] = ref
		}
	}
	// Branch-style: the threaded value leaves by the decision port, not both.
	carried := output["context"]
	delete(output, "context")
	decisionPort := "approved"
	if decision.Decision == "reject" {
		decisionPort = "rejected"
	}
	output[decisionPort] = carried
	output["approver"] = core.Ref{MIME: "text/plain", Inline: decision.Approver}
	output["comment"] = core.Ref{MIME: "text/plain", Inline: decision.Comment}

	resumeResult := &core.Result{
		JobID:  nodeRecID,
		Status: core.StatusOK,
		Output: output,
	}
	if err := s.Jobs.Complete(ctx, nodeRecID, core.JobStatusSucceeded, resumeResult); err != nil {
		return fmt.Errorf("complete: %w", err)
	}

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
	// Nothing else will wake a worker here: no step just finished on one.
	s.Wake.Notify()
	// Put the run back to Running, or it keeps claiming to be waiting.
	if !runHasParkedApproval(ctx, s.Jobs, graphRunID, nodeID) {
		setRunParked(ctx, s.Jobs, s.Logger, graphRunID, false)
	}
	// After the resume commits, so a failed resume does not mail a decision.
	s.NotifyApprovalDecided(ctx, g, graphRunID, nodeID, decision)
	return nil
}

type ApprovalListener struct {
	svc    *Service
	signer *HMACApprovalSigner
	logger *log.Logger

	Audit core.AuditLog
}

func NewApprovalListener(svc *Service, signer *HMACApprovalSigner) *ApprovalListener {
	return &ApprovalListener{
		svc:    svc,
		signer: signer,
		logger: log.New(log.Writer(), "approve: ", log.LstdFlags),
	}
}

func (a *ApprovalListener) auditDecision(ctx context.Context, graphRunID, nodeID, decision, approver string) {
	if a.Audit == nil {
		return
	}
	tenant := ""
	if rec, err := a.svc.Jobs.Get(ctx, graphRunID); err == nil {
		tenant = rec.Tenant
	}
	actor := strings.TrimSpace(approver)
	if actor == "" {
		actor = "(unidentified link holder)"
	} else {
		actor += " (self-declared, via approval link)"
	}
	if err := a.Audit.Append(ctx, core.AuditEvent{
		Time:   time.Now(),
		Tenant: tenant,
		Actor:  sanitizeAuditField(actor),
		Action: "approval",
		Target: sanitizeAuditField(graphRunID + "/" + nodeID),
		Detail: sanitizeAuditField(decision),
	}); err != nil {
		a.logger.Printf("audit append (approval %s/%s): %v", graphRunID, nodeID, err)
	}
}

func (a *ApprovalListener) handle(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	graphRunID, nodeID, ok := parseApprovalPath(rw, r)
	if !ok {
		return
	}
	if !a.tokenOK(r, graphRunID, nodeID) {
		a.denyApproval(rw, r, http.StatusUnauthorized, "invalid or expired token")
		return
	}
	decision := approvalField(r, "decision")
	if decision == "" {
		decision = "approve"
	}
	// Only after the token verifies: an unauthenticated caller learns nothing.
	if decision != "approve" && decision != "reject" {
		a.denyApproval(rw, r, http.StatusBadRequest, "decision must be approve or reject")
		return
	}
	approver := approvalField(r, "approver")
	comment := approvalField(r, "comment")

	if err := a.svc.Approve(r.Context(), graphRunID, nodeID, ApprovalDecision{
		Decision: decision,
		Approver: approver,
		Comment:  comment,
	}); err != nil {
		a.logger.Printf("approve %s/%s: %v", graphRunID, nodeID, err)
		switch {
		case errors.Is(err, core.ErrConflict):
			if wantsHTML(r) {
				lang, _, _ := a.approvalPageState(r, graphRunID, nodeID)
				renderApproval(rw, http.StatusConflict, approvalView{
					Lang: maillang.Primary(lang), M: maillang.For(lang), Already: true,
				})
				return
			}
			http.Error(rw, err.Error(), http.StatusConflict)
		case errors.Is(err, core.ErrNotFound):
			a.denyApproval(rw, r, http.StatusNotFound, err.Error())
		default:
			a.denyApproval(rw, r, http.StatusInternalServerError, err.Error())
		}
		return
	}
	a.auditDecision(r.Context(), graphRunID, nodeID, decision, approver)
	a.logger.Printf("resumed %s/%s decision=%s approver=%s", graphRunID, nodeID, decision, approver)
	if wantsHTML(r) {
		lang, _, _ := a.approvalPageState(r, graphRunID, nodeID)
		renderApproval(rw, http.StatusOK, approvalView{
			Lang: maillang.Primary(lang), M: maillang.For(lang),
			Done: true, Decision: decision,
		})
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(rw).Encode(map[string]string{
		"status":   "resumed",
		"decision": decision,
	})
}

func parseApprovalPath(rw http.ResponseWriter, r *http.Request) (graphRunID, nodeID string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/approve/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(rw, "expected /approve/<graphRunID>/<nodeID>", http.StatusBadRequest)
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (a *ApprovalListener) tokenOK(r *http.Request, graphRunID, nodeID string) bool {
	exp, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
	if err != nil {
		return false
	}
	token := r.URL.Query().Get("token")
	return token != "" && a.signer.verifyToken(graphRunID, nodeID, exp, token)
}

func approvalField(r *http.Request, name string) string {
	if v := r.URL.Query().Get(name); v != "" {
		return v
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		if err := r.ParseForm(); err == nil {
			return r.PostFormValue(name)
		}
	}
	return ""
}

func (a *ApprovalListener) denyApproval(rw http.ResponseWriter, r *http.Request, status int, msg string) {
	if wantsHTML(r) {
		renderApproval(rw, status, approvalView{Lang: "en", M: maillang.English, Gone: true})
		return
	}
	http.Error(rw, msg, status)
}

func ServeApprovalForTest(a *ApprovalListener, rw http.ResponseWriter, r *http.Request) {
	a.handle(rw, r)
}

func ServeApprovalPageForTest(a *ApprovalListener, rw http.ResponseWriter, r *http.Request) {
	a.handleApprovalPage(rw, r)
}
