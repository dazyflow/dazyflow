package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "stripe_list_events",
			Version:     "1.0",
			Label:       "Stripe",
			Subtitle:    "List events",
			Summary:     "Read recent Stripe events — the building block for 'fire on new payment / failed invoice / new subscription' flows.",
			Description: "List your account's recent events (newest first), optionally filtered to specific types like payment_intent.succeeded or invoice.payment_failed. For a trigger, compose: Schedule/Poll → this step (wire the saved cursor into 'After id') → For each event → … → Set secret with 'Last id'. Only events newer than the cursor are returned, so each poll sees each event once. With no cursor it returns the most recent events.",
			Integration: "Stripe",
			Category:    "network",
			Icon:        "credit-card",
			BrandLogo:   "/brands/stripe.svg",
			Color:       "#635BFF",
			Provider:    "internal",
			Tags:        []string{"stripe", "events", "trigger", "poll", "webhook", "billing"},
			Examples: []core.ParamsExample{
				{Title: "New successful payments", Params: json.RawMessage(`{"types":["payment_intent.succeeded"]}`), Notes: "Wire ${secret.STRIPE_EVENT_CURSOR} into 'After id' and the 'Last id' output into a Set secret step named STRIPE_EVENT_CURSOR."},
				{Title: "Billing trouble feed", Params: json.RawMessage(`{"types":["invoice.payment_failed","customer.subscription.deleted"],"limit":50}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "STRIPE_API_KEY", Note: "Stripe secret API key (sk_live_… / sk_test_…)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "after_id", Label: "After ID", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "events", Label: "Events", MIME: []string{"application/json"}},
				{Port: "last_id", Label: "Last ID", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"api_key":{"type":"string","title":"API key","default":"${secret.STRIPE_API_KEY}","x_advanced":true,"description":"Stripe secret key. The default reads the STRIPE_API_KEY secret."},
					"types":{"type":"array","title":"Event types","format":"string-multiselect","items":{"type":"string","enum":["payment_intent.succeeded","payment_intent.payment_failed","charge.refunded","charge.dispute.created","invoice.paid","invoice.payment_failed","customer.subscription.created","customer.subscription.updated","customer.subscription.deleted","customer.created","checkout.session.completed","payout.paid"],"enumNames":["Payment succeeded","Payment failed","Charge refunded","Dispute opened","Invoice paid","Invoice payment failed","Subscription created","Subscription updated","Subscription canceled","Customer created","Checkout completed","Payout paid"]},"description":"Pick the events to watch for. Empty = every event. Need one that's not listed? Add it as a custom type (Stripe's dotted name, e.g. payout.failed)."},
					"limit":{"type":"integer","title":"Limit","default":25,"minimum":1,"maximum":100,"description":"Max events per poll."},
					"after_id":{"type":"string","title":"After id","description":"Only events newer than this event id (evt_…). Overridden by the 'After id' input — usually a saved cursor secret."},
					"base_url":{"type":"string","description":"Override the API host (testing)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				},
				"required":["api_key"]
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeListEvents,
	})
}

func executeListEvents(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	rawAfterID, ok := textInputOr(job, "after_id", params.StringDefault(job.Params, "after_id", ""))
	if !ok {
		return params.Err(job, "bad_input", "'After id' input must be text"), nil
	}
	afterID := rawAfterID
	limit := params.IntDefault(job.Params, "limit", 25)
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}

	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))
	// Stripe's event list is newest-first; ending_before=<cursor> returns
	// only entries NEWER than the cursor — exactly the poll semantics.
	// Anything that isn't an event id (evt_…) is treated as "no cursor":
	// the cursor-secret pattern needs a placeholder value on the very
	// first run (a secret has to exist before ${secret.…} resolves), and
	// sending that placeholder to Stripe would 400 the whole poll.
	if !strings.HasPrefix(afterID, "evt_") {
		afterID = ""
	}
	if afterID != "" {
		q.Set("ending_before", afterID)
	}
	if types, ok := job.Params["types"].([]any); ok {
		for i, tv := range types {
			if s, ok := tv.(string); ok && s != "" {
				q.Set(fmt.Sprintf("types[%d]", i), s)
			}
		}
	}

	status, body, err := stripeDo(ctx, job, http.MethodGet, baseURL(job)+"/events?"+q.Encode(), "")
	if r := stripeFailure(job, status, body, err); r != nil {
		return *r, nil
	}
	var parsed struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return params.Err(job, "stripe_error", "could not decode Stripe events response"), nil
	}
	// last_id is the NEWEST seen event — the next poll's cursor. With no
	// new events it echoes the incoming cursor (placeholder included, so
	// the first idle poll round-trips it), and a Set-secret step
	// downstream never clobbers a good cursor with an empty value.
	lastID := rawAfterID
	if len(parsed.Data) > 0 {
		if id, ok := parsed.Data[0]["id"].(string); ok {
			lastID = id
		}
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"events":  {MIME: "application/json", Inline: parsed.Data},
			"last_id": {MIME: "text/plain", Inline: lastID},
			"meta": {MIME: "application/json", Inline: map[string]any{
				"count": len(parsed.Data), "cursor_in": afterID, "cursor_out": lastID,
			}},
		},
	}, nil
}
