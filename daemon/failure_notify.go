package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// Failure-notification dispatcher. Listens on the bus for a single
// run and, when the run terminates with status=failed, POSTs a
// concise payload to the graph's configured webhook URL.
//
// Design choices:
//
//   - Per-run goroutine spawned at SubmitGraph time. The bus
//     subscribes by jobID, so a "watch all runs" listener would
//     require a different bus contract; the per-run goroutine
//     model fits today's surface.
//
//   - Best-effort delivery: one POST attempt with a 10s timeout,
//     errors logged but not retried. A retry loop would mean
//     persistent state (where to retry, how many times left); for
//     v1 the user re-runs the workflow if they want another shot.
//     The next phase upgrades this to fire through the existing
//     webhook_send drop so retry policy is shared.
//
//   - HTTP only: SSRF risk is non-zero (user-supplied URL), but
//     since the user configures it on their own graph that's an
//     accepted shape — they're not tricking another tenant into
//     POSTing to their internal services. A deployment-wide
//     allowlist (matching the http_request drop's SSRF blocks)
//     would be a future hardening.

// failureNotifyClient is the HTTP client the notifier uses.
// Variable rather than a const so tests can swap in an httptest
// server's client without injecting it through five layers.
var failureNotifyClient = &http.Client{Timeout: 10 * time.Second}

// FailurePayload is the JSON shape POSTed to the configured
// webhook URL. Compact on purpose — receivers (Slack, PagerDuty,
// internal Lambdas) want structured fields they can route on, not
// a freeform message.
type FailurePayload struct {
	GraphID      string `json:"graph_id"`
	RunID        string `json:"run_id"`
	Tenant       string `json:"tenant"`
	Workspace    string `json:"workspace"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	FailedNode   string `json:"failed_node,omitempty"`
	RunURL       string `json:"run_url,omitempty"`
	FinishedAt   string `json:"finished_at,omitempty"`
}

// startFailureNotifier kicks off the per-run goroutine that
// watches for terminal+failed and fires the notification. Returns
// immediately; the goroutine self-terminates on terminal events,
// context cancellation, or bus closure.
//
// No-op when the graph has no FailureNotify or no Webhook —
// avoids spawning a goroutine that would just exit on the first
// event. Important because some hot deployment loops trigger many
// graphs.
func (s *Service) startFailureNotifier(graph core.Graph, runID string) {
	if graph.FailureNotify == nil || graph.FailureNotify.Webhook == "" {
		return
	}
	// Subscribe SYNCHRONOUSLY before spawning the goroutine so a
	// dispatcher that publishes the terminal event the next
	// nanosecond after this returns can't race past us. The
	// race-recheck inside watchForFailure also handles the
	// "already done" case (worker finished between Enqueue and
	// here), so both ends are covered.
	events, cancelSub := s.bus().Subscribe(runID)
	// 1-hour bound. The caller's request ctx would be cancelled the
	// moment SubmitGraph returns, killing the watcher before the
	// graph even runs.
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	go func() {
		defer cancel()
		defer cancelSub()
		s.watchForFailure(ctx, graph, runID, events)
	}()
}

func (s *Service) watchForFailure(ctx context.Context, graph core.Graph, runID string, events <-chan BusEvent) {

	// Defensive recheck: the worker might have completed between
	// SubmitGraph and our subscribe — same race WaitGraph defends
	// against. Pull the record and short-circuit if it's already
	// terminal.
	if rec, err := s.Jobs.Get(ctx, runID); err == nil && isTerminal(rec.Status) {
		if rec.Status == core.JobStatusFailed {
			s.fireFailureNotification(ctx, graph, recToPayload(graph, rec, s.PublicBaseURL))
		}
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				// Bus closed without a terminal event we observed —
				// fall back to a final record read in case the worker
				// completed during the gap.
				if rec, err := s.Jobs.Get(context.Background(), runID); err == nil &&
					rec.Status == core.JobStatusFailed {
					s.fireFailureNotification(context.Background(), graph,
						recToPayload(graph, rec, s.PublicBaseURL))
				}
				return
			}
			if ev.Terminal == nil {
				continue
			}
			if ev.Terminal.Status == core.JobStatusFailed {
				// Look up the node-level failure to populate FailedNode.
				payload := terminalToPayload(graph, runID, ev.Terminal, s.PublicBaseURL)
				if payload.FailedNode == "" {
					// Bus event doesn't carry the failed node ID; query
					// the store. ListNodeRecords with Status=failed +
					// the run filter is exactly the shape we just
					// added for run-detail.
					if nodes, err := s.Jobs.ListNodeRecords(ctx, core.ListNodeRecordsOpts{
						Tenant:     graph.Tenant,
						Workspace:  graph.Workspace,
						GraphRunID: runID,
						Status:     core.JobStatusFailed,
						Limit:      1,
					}); err == nil && len(nodes) > 0 {
						payload.FailedNode = nodes[0].NodeID
					}
				}
				s.fireFailureNotification(ctx, graph, payload)
			}
			return
		}
	}
}

// fireFailureNotification POSTs the payload to the graph's
// webhook URL. Errors land in the daemon log; we don't surface
// them back to the user because the dispatcher runs after the run
// has already terminated — there's no in-progress request to fail.
func (s *Service) fireFailureNotification(ctx context.Context, graph core.Graph, payload FailurePayload) {
	url := graph.FailureNotify.Webhook
	body, err := json.Marshal(payload)
	if err != nil {
		s.logFailureNotifyError(graph, fmt.Errorf("marshal: %w", err))
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		s.logFailureNotifyError(graph, fmt.Errorf("build request: %w", err))
		return
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "hazyflow-failure-notify/1.0")
	resp, err := failureNotifyClient.Do(req)
	if err != nil {
		s.logFailureNotifyError(graph, fmt.Errorf("post: %w", err))
		return
	}
	defer resp.Body.Close()
	// Drain so the connection can be reused.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.logFailureNotifyError(graph, fmt.Errorf("non-2xx status %d", resp.StatusCode))
	}
}

func (s *Service) logFailureNotifyError(graph core.Graph, err error) {
	// Service doesn't carry its own logger today; log to the
	// standard log package which the gateway's middleware shares.
	// Format matches the rest of the daemon's structured-ish logs.
	if s.Logger != nil {
		s.Logger.Printf("failure-notify [%s/%s/%s]: %v",
			graph.Tenant, graph.Workspace, graph.ID, err)
	}
}

// recToPayload populates a FailurePayload from a record that's
// already terminal (the "race-recheck" path).
func recToPayload(graph core.Graph, rec core.JobRecord, baseURL string) FailurePayload {
	p := FailurePayload{
		GraphID:   graph.ID,
		RunID:     rec.ID,
		Tenant:    graph.Tenant,
		Workspace: graph.Workspace,
	}
	if rec.Result != nil && rec.Result.Error != nil {
		p.ErrorCode = rec.Result.Error.Code
		p.ErrorMessage = rec.Result.Error.Message
	}
	if rec.FinishedAt != nil {
		p.FinishedAt = rec.FinishedAt.UTC().Format(time.RFC3339)
	}
	p.RunURL = buildRunURL(baseURL, rec.ID)
	return p
}

// terminalToPayload populates a FailurePayload from a TerminalEvent
// observed off the bus (the common path — the watcher's actually
// watching for these).
func terminalToPayload(graph core.Graph, runID string, t *TerminalEvent, baseURL string) FailurePayload {
	p := FailurePayload{
		GraphID:    graph.ID,
		RunID:      runID,
		Tenant:     graph.Tenant,
		Workspace:  graph.Workspace,
		FinishedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if t.Error != nil {
		p.ErrorCode = t.Error.Code
		p.ErrorMessage = t.Error.Message
	}
	p.RunURL = buildRunURL(baseURL, runID)
	return p
}

// buildRunURL constructs a UI-facing link to the run-detail page
// when the daemon knows its own public origin. Empty string when
// PublicBaseURL isn't set — receivers should fall back to the
// graph_id/run_id fields in that case.
func buildRunURL(baseURL, runID string) string {
	if baseURL == "" {
		return ""
	}
	return strings.TrimRight(baseURL, "/") + "/runs/" + runID
}
