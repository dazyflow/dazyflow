package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"git.sr.ht/~klahr/hazy-flow/core"
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
	sched, err := cronValidator.Parse(expr)
	if err != nil {
		writeJSON(rw, http.StatusOK, cronValidateResponse{
			Valid: false,
			Error: err.Error(),
		})
		return
	}
	// Compute the next N fire times anchored at "now". UTC keeps the
	// preview deterministic regardless of where the daemon runs;
	// the UI renders these to local time if it wants.
	now := time.Now().UTC()
	fires := make([]string, 0, nextFiresPreview)
	t := now
	for i := 0; i < nextFiresPreview; i++ {
		t = sched.Next(t)
		if t.IsZero() {
			break
		}
		fires = append(fires, t.Format(time.RFC3339))
	}
	writeJSON(rw, http.StatusOK, cronValidateResponse{
		Valid:     true,
		NextFires: fires,
	})
}
