package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"git.sr.ht/~klahr/hazyflow/core"
)

// cronValidator uses the SAME parser the scheduler uses (5-field
// minute-hour-dom-month-dow), so anything that passes here is also
// fireable by the scheduler at rescan time. Built once at startup —
// the parser holds no state, so a package-level value is fine.
var cronValidator = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// nextFiresPreview is how many upcoming fire times the validate
// endpoint returns when an expression parses. Three is enough to
// confirm the cadence ("daily" / "every Monday") without sending
// back a wall of timestamps for short-interval expressions.
const nextFiresPreview = 3

type cronValidateRequest struct {
	Expr string `json:"expr"`
	// TZ is the IANA timezone the expression is interpreted in, so the
	// preview matches the time the scheduler will actually fire (which
	// reads the same field off the saved trigger). Empty defaults to UTC.
	// The web editor sends its browser timezone here.
	TZ string `json:"tz,omitempty"`
}

type cronValidateResponse struct {
	Valid     bool     `json:"valid"`
	Error     string   `json:"error,omitempty"`
	NextFires []string `json:"next_fires,omitempty"` // up to 3, RFC3339 UTC
}

// validateCron parses a 5-field cron expression and returns whether
// the scheduler would accept it, plus the next few fire times as a
// preview so the UI can show users WHEN the flow will run.
//
// The endpoint deliberately does not save anything — graphs persist
// through PUT /graphs as usual; this is a pre-save sanity check that
// keeps users from saving an expression that silently never fires.
func (h *HTTPGateway) validateCron(rw http.ResponseWriter, r *http.Request, _ core.Principal) {
	var body cronValidateRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}
	expr := strings.TrimSpace(body.Expr)
	if expr == "" {
		writeJSON(rw, http.StatusOK, cronValidateResponse{
			Valid: false,
			Error: "expression is empty",
		})
		return
	}
	sched, err := parseCronInTZ(cronValidator, expr, body.TZ)
	if err != nil {
		writeJSON(rw, http.StatusOK, cronValidateResponse{
			Valid: false,
			Error: err.Error(),
		})
		return
	}
	// Compute the next N fire times. The schedule already carries the
	// requested timezone (via parseCronInTZ), so sched.Next anchors the
	// wall-clock fields to that zone; we just need a current instant to
	// start from. Returned as UTC instants — the UI renders them in the
	// viewer's local clock, which for our own editor IS the timezone we
	// evaluated in, so "every day at 09:00" previews as 09:00.
	writeJSON(rw, http.StatusOK, cronValidateResponse{
		Valid:     true,
		NextFires: nextCronFires(sched, time.Now(), nextFiresPreview),
	})
}

// nextCronFires returns up to n upcoming fire times for sched starting
// from `from`, as RFC3339 UTC strings. Shared by the cron-validate
// endpoint (the pre-save preview) and the schedules listing (the live
// "next run" column) so the time a user previews and the time the flow
// actually fires are computed by one code path. Stops early if the
// schedule gives up (zero time — an impossible date like Feb 30).
func nextCronFires(sched cron.Schedule, from time.Time, n int) []string {
	fires := make([]string, 0, n)
	t := from
	for i := 0; i < n; i++ {
		t = sched.Next(t)
		if t.IsZero() {
			break
		}
		fires = append(fires, t.UTC().Format(time.RFC3339))
	}
	return fires
}
