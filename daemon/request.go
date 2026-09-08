// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

const (
	// defaultCallWait is how long the endpoint holds a connection when the
	// caller doesn't say. Long enough for a flow that does real work before
	// replying, short enough to sit inside a default client timeout.
	defaultCallWait = 30 * time.Second
	// maxCallWait caps ?wait. Past this a caller should submit and poll:
	// every waiter costs a held connection and a goroutine.
	maxCallWait      = 60 * time.Second
	callPollInterval = 2 * time.Second
	// callNodeRecordLimit bounds the per-poll node-record read. A graph
	// larger than this is past the node ceiling anyway.
	callNodeRecordLimit = 1000
)

func (w *WebhookListener) handleCall(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/call/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		http.Error(rw, "expected /call/<tenant>/<workspace>/<graph-id>", http.StatusBadRequest)
		return
	}
	tenant, workspace, graphID := parts[0], parts[1], parts[2]

	// One generic 401 for every pre-authentication failure, so an
	// unauthenticated caller can't enumerate which flows exist. Same stance
	// as handleTrigger.
	const unauthorized = "unknown endpoint or invalid secret"

	store, err := w.svc.Workspaces.Open(tenant, workspace)
	if err != nil {
		http.Error(rw, unauthorized, http.StatusUnauthorized)
		return
	}
	g, err := store.LoadPublished(graphID)
	if err != nil {
		http.Error(rw, unauthorized, http.StatusUnauthorized)
		return
	}
	keys := core.GraphRequestSecrets(g)
	switch {
	case len(keys) > 0:
		if !anyKeyMatches(keys, webhookKey(r)) {
			http.Error(rw, unauthorized, http.StatusUnauthorized)
			return
		}
	case core.GraphRequestPublic(g):
	default:
		http.Error(rw, unauthorized, http.StatusUnauthorized)
		return
	}
	if g.Disabled {
		http.Error(rw, `{"error":{"code":"flow_disabled","message":"flow is currently disabled — re-enable via enable_flow"}}`, http.StatusForbidden)
		return
	}

	rawBody, ok := w.readCallBody(rw, r)
	if !ok {
		return
	}
	wait := callWait(r)

	// Before anything is submitted: a retry carrying a key we've seen either
	// replays the answer or joins the run it already started.
	claim, handled := w.claimCall(rw, r, g, rawBody, wait)
	if handled {
		return
	}
	defer claim.release()

	seed := buildWebhookSeed(rawBody, r)
	seeds := map[string]core.Result{}
	inputs := 0
	for _, n := range g.Nodes {
		if n.Module != core.RequestInputModule {
			continue
		}
		inputs++
		if triggerNodeDisabled(n) {
			continue
		}
		seeds[n.ID] = seed
	}
	if inputs > 0 && len(seeds) == 0 {
		http.Error(rw, `{"error":{"code":"trigger_disabled","message":"this flow's Request step is turned off — re-enable the step to accept calls"}}`, http.StatusForbidden)
		return
	}

	principal := SystemPrincipal("dazyflow-request", g.Tenant, g.Workspace)
	// The submit itself must not be abandoned half-written if the caller
	// hangs up between here and the first byte of the response.
	runID, err := w.svc.SubmitGraphOpts(context.WithoutCancel(r.Context()), principal, g, SubmitOpts{
		Seeds:        seeds,
		TriggerDepth: inboundTriggerDepth(r),
	})
	if errors.Is(err, core.ErrTriggerLoop) {
		w.logger.Printf("refused %s/%s/%s: %v", tenant, workspace, graphID, err)
		http.Error(rw, `{"error":{"code":"trigger_loop","message":"this call came from a run this flow started — the chain is too deep, so a flow is calling itself"}}`,
			http.StatusTooManyRequests)
		return
	}
	if err != nil {
		w.logger.Printf("submit %s/%s/%s: %v", tenant, workspace, graphID, err)
		http.Error(rw, fmt.Sprintf("submit: %v", err), http.StatusInternalServerError)
		return
	}
	w.logger.Printf("called %s/%s/%s → %s (%d request seed(s))", tenant, workspace, graphID, runID, len(seeds))
	// From here a retry of this key must join this run, never start another.
	claim.attach(runID)

	if wait <= 0 {
		writeCallPending(rw, runID)
		return
	}
	cw := &captureWriter{ResponseWriter: rw, headers: http.Header{}}
	w.awaitReply(cw, r, g, runID, wait)
	claim.commit(cw)
}

func (w *WebhookListener) readCallBody(rw http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.Body == nil {
		return nil, true
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, w.MaxBodyBytes+1))
	_ = r.Body.Close()
	if err != nil {
		http.Error(rw, "read body", http.StatusBadRequest)
		return nil, false
	}
	if int64(len(data)) > w.MaxBodyBytes {
		http.Error(rw, fmt.Sprintf("body exceeds %d bytes", w.MaxBodyBytes), http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return data, true
}

func (w *WebhookListener) awaitReply(rw http.ResponseWriter, r *http.Request, g core.Graph, runID string, wait time.Duration) {
	replies := map[string]core.Node{}
	for _, n := range g.Nodes {
		if n.Module == core.ReplyModule {
			replies[n.ID] = n
		}
	}

	events, cancelSub := w.svc.bus().Subscribe(runID)
	defer cancelSub()

	ctx := r.Context()
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	ticker := time.NewTicker(callPollInterval)
	defer ticker.Stop()

	check := true
	for {
		if check {
			if done := w.settleCall(ctx, rw, runID, replies); done {
				return
			}
			check = false
		}
		select {
		case ev, open := <-events:
			if !open {
				events = nil
				check = true
				continue
			}
			check = callEventSettles(ev, replies)
		case <-ticker.C:
			check = true
		case <-deadline.C:
			writeCallPending(rw, runID)
			return
		case <-ctx.Done():
			return
		}
	}
}

func callEventSettles(ev BusEvent, replies map[string]core.Node) bool {
	if ev.Terminal != nil {
		return true
	}
	if ev.NodeStatus == nil {
		return false
	}
	_, isReply := replies[ev.NodeStatus.NodeID]
	return isReply
}

func (w *WebhookListener) settleCall(ctx context.Context, rw http.ResponseWriter, runID string, replies map[string]core.Node) bool {
	if len(replies) > 0 {
		recs, err := w.svc.Jobs.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
			GraphRunID: runID,
			Limit:      callNodeRecordLimit,
		})
		if err == nil {
			for _, rec := range recs {
				n, isReply := replies[rec.NodeID]
				if !isReply || rec.Status != core.JobStatusSucceeded || rec.Result == nil {
					continue
				}
				if ref, ok := rec.Result.Output["body"]; ok {
					writeReply(rw, core.ReplyStatusCode(n.Params), ref)
					return true
				}
			}
		}
	}
	run, err := w.svc.Jobs.Get(ctx, runID)
	if err != nil || !core.IsTerminalStatus(run.Status) {
		return false
	}
	status := http.StatusOK
	if run.Status != core.JobStatusSucceeded {
		status = http.StatusBadGateway
	}
	body := map[string]any{"run_id": runID, "status": string(run.Status)}
	if run.Result != nil && run.Result.Error != nil {
		body["error"] = run.Result.Error
	}
	writeJSON(rw, status, body)
	return true
}

func writeReply(rw http.ResponseWriter, status int, ref core.Ref) {
	mime := ref.MIME
	if mime == "" {
		mime = "text/plain; charset=utf-8"
	}
	var payload []byte
	switch v := ref.Inline.(type) {
	case string:
		payload = []byte(v)
	case []byte:
		payload = v
	case nil:
		payload = nil
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			writeJSONError(rw, http.StatusInternalServerError, "reply value could not be encoded")
			return
		}
		payload, mime = encoded, "application/json"
	}
	rw.Header().Set("Content-Type", mime)
	rw.WriteHeader(status)
	_, _ = rw.Write(payload)
}

func writeCallPending(rw http.ResponseWriter, runID string) {
	writeJSON(rw, http.StatusAccepted, map[string]string{"run_id": runID, "status": "running"})
}

// callWait reads ?wait=<seconds>. Absent means the default; 0 means "don't
// wait", which is how a caller that must not block opts out.
func callWait(r *http.Request) time.Duration {
	raw := strings.TrimSpace(r.URL.Query().Get("wait"))
	if raw == "" {
		return defaultCallWait
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs < 0 {
		return defaultCallWait
	}
	if d := time.Duration(secs) * time.Second; d < maxCallWait {
		return d
	}
	return maxCallWait
}

func ServeCallForTest(w *WebhookListener, rw http.ResponseWriter, r *http.Request) {
	w.handleCall(rw, r)
}

// Idempotency-Key on /call
//
// A caller whose client gave up at 10 seconds and retried is the case this
// exists for: without a key that retry starts a SECOND run, and the flow's
// side effects happen twice. With one, the retry joins the run its first
// request started and waits for the same answer.
//
// Keys are scoped to the flow rather than to a caller identity — /call has no
// principal, its credential is the flow's own bearer key. So every holder of a
// valid key for one flow shares that flow's key namespace, which is the same
// trust boundary as being able to call it at all.

type callClaim struct {
	store *idempotencyStore
	key   string
	// attached is set once a run exists under this key. From that moment the
	// reservation must NOT be released: a retry has to join that run, and
	// releasing would let it start another — the duplicate this prevents.
	attached bool
	resolved bool
}

func (c *callClaim) attach(runID string) {
	if c == nil {
		return
	}
	c.store.attach(c.key, runID)
	c.attached = true
}

// commit caches a settled answer for replay. Two rules differ from the generic
// middleware's: a 202 ("still running") is never cached, because a retry should
// join the run and wait again rather than be told forever that it is pending;
// and a failed run IS cached, because its side effects already happened — a
// caller who wants to genuinely try again mints a new key.
func (c *callClaim) commit(cw *captureWriter) {
	if c == nil || cw.status == 0 || cw.status == http.StatusAccepted {
		return
	}
	c.store.commit(c.key, &idempotentResponse{
		status:   cw.status,
		headers:  cw.headers,
		body:     cw.body.Bytes(),
		storedAt: time.Now(),
	})
	c.resolved = true
}

// release drops a reservation that never became a run — a submit that failed,
// or a handler that returned before starting one. Once a run is attached the
// reservation stands until it expires.
func (c *callClaim) release() {
	if c == nil || c.attached || c.resolved {
		return
	}
	c.store.abort(c.key)
}

// claimCall resolves the Idempotency-Key header before anything is submitted.
// handled is true once it has answered the caller itself — by replaying a
// cached response, by joining the run a previous request with this key
// started, or by refusing a reused key. A nil claim with handled=false means
// the caller sent no key and gets the ordinary at-least-once behaviour.
func (w *WebhookListener) claimCall(
	rw http.ResponseWriter,
	r *http.Request,
	g core.Graph,
	rawBody []byte,
	wait time.Duration,
) (*callClaim, bool) {
	key := strings.TrimSpace(r.Header.Get(idempotencyHeader))
	if key == "" || w.idempotency == nil {
		return nil, false
	}
	if len(key) > idempotencyKeyMax {
		writeJSONError(rw, http.StatusBadRequest,
			fmt.Sprintf("Idempotency-Key must be <= %d chars", idempotencyKeyMax))
		return nil, true
	}
	cacheKey := "call|" + g.Tenant + "/" + g.Workspace + "/" + g.ID + "|" + key
	reqHash := callRequestHash(r, rawBody)

	existing, fresh := w.idempotency.begin(cacheKey, reqHash)
	if fresh {
		return &callClaim{store: w.idempotency, key: cacheKey}, false
	}
	// Same key, different payload is misuse: replaying the first answer would
	// hide it, so say so instead.
	if existing.reqHash != "" && existing.reqHash != reqHash {
		rw.Header().Set("Idempotency-Replay", "false")
		writeJSONError(rw, http.StatusUnprocessableEntity,
			"this Idempotency-Key was already used with a different request body")
		return nil, true
	}
	if existing.done {
		for k, vs := range existing.headers {
			for _, v := range vs {
				rw.Header().Add(k, v)
			}
		}
		rw.Header().Set("Idempotency-Replay", "true")
		rw.WriteHeader(existing.status)
		_, _ = rw.Write(existing.body)
		return nil, true
	}
	if existing.runID != "" {
		// The retry this whole mechanism is for: wait on the first request's
		// run instead of starting a second one.
		rw.Header().Set("Idempotency-Replay", "true")
		if wait <= 0 {
			writeCallPending(rw, existing.runID)
			return nil, true
		}
		w.awaitReply(rw, r, g, existing.runID, wait)
		return nil, true
	}
	// Reserved, but the run doesn't exist yet — a concurrent duplicate caught
	// in the window between begin and submit. Refusing is the only answer that
	// can't double-fire.
	rw.Header().Set("Idempotency-Replay", "false")
	writeJSONError(rw, http.StatusConflict,
		"a call with this Idempotency-Key is already being processed; retry shortly")
	return nil, true
}

func callRequestHash(r *http.Request, rawBody []byte) string {
	sum := sha256.New()
	sum.Write([]byte(r.Method))
	sum.Write([]byte("\n"))
	sum.Write([]byte(r.URL.Path))
	sum.Write([]byte("\n"))
	sum.Write(rawBody)
	return hex.EncodeToString(sum.Sum(nil))
}
