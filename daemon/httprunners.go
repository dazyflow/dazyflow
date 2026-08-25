// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Two audiences, two kinds of credential, and they must not be confused.
//
//	The ADMIN endpoints are used by a person in the web UI (or an API key), and
//	go through the normal session/key auth like every other admin route.
//
//	The RUNNER endpoints are used by an agent on someone's machine, holding a
//	credential that identifies exactly one runner and authorises nothing else.
//	They sit OUTSIDE requireAuth: an agent has no session, no user, and no
//	permissions, and giving it a normal API key would hand a machine in a
//	cupboard the ability to read flows.
//
// The asymmetry is the point. A stolen runner credential lets someone claim
// that runner's tasks — bad, but bounded, and revoked by deleting the runner.

// ---- admin side -------------------------------------------------------

// runnerRow is the admin list's shape. There is no credential in it and no
// field for one: the agent's credential is shown once, at registration, and
// never again.
type runnerRow struct {
	Name    string   `json:"name"`
	Labels  []string `json:"labels,omitempty"`
	Version string   `json:"version,omitempty"`
	// Online is derived from LastSeen rather than reported, because there is no
	// connection to observe — a runner is present if it has asked for work
	// recently.
	Online    bool      `json:"online"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// requireRunnerAdmin gates the admin endpoints.
//
// organization:admin is what a human managing the org already holds;
// module:register is what an API key for automation can carry without also
// being able to administer everything else. Deliberately NOT graph:edit —
// registering a runner is what makes a machine available at all, which is a
// different act from using it in a flow.
func requireRunnerAdmin(rw http.ResponseWriter, p core.Principal) bool {
	if core.CanAdminOrg(p) || p.Has(core.PermModuleRegister) {
		return true
	}
	writeAPIError(rw, http.StatusForbidden, "forbidden",
		"organization:admin or module:register required")
	return false
}

func (h *HTTPGateway) runnersConfigured(rw http.ResponseWriter) bool {
	if h.Runners == nil || h.Runners.Store == nil {
		writeJSONError(rw, http.StatusNotImplemented, "runners are not configured on this deployment")
		return false
	}
	return true
}

// runnerTasksConfigured is the agent endpoints' gate: the registry AND the
// queue. One function rather than `!h.runnersConfigured(rw) || h.RunnerTasks
// == nil` followed by a write, which sent the 501 body TWICE when the registry
// was the missing half — two concatenated JSON envelopes in one response.
func (h *HTTPGateway) runnerTasksConfigured(rw http.ResponseWriter) bool {
	if !h.runnersConfigured(rw) {
		return false
	}
	if h.RunnerTasks == nil {
		writeJSONError(rw, http.StatusNotImplemented, "runners are not configured on this deployment")
		return false
	}
	return true
}

func (h *HTTPGateway) listRunners(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireRunnerAdmin(rw, p) || !h.runnersConfigured(rw) {
		return
	}
	rows, err := h.Runners.List(r.Context(), p.Tenant)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	out := make([]runnerRow, 0, len(rows))
	for _, x := range rows {
		out = append(out, runnerRow{
			Name:      x.Name,
			Labels:    x.Labels,
			Version:   x.Version,
			Online:    x.Online(now),
			LastSeen:  x.LastSeen,
			CreatedBy: x.CreatedBy,
			CreatedAt: x.CreatedAt,
		})
	}
	writeJSON(rw, http.StatusOK, map[string]any{"runners": out})
}

// runnerTargetRow is the flow editor's shape: what a step needs to choose where
// to run, and nothing else.
//
// Deliberately narrower than runnerRow. Who registered a machine, when, and
// which agent version it reported are facts about administering the fleet; a
// picker in the inspector needs the name, the labels it can be targeted by, and
// whether it is there right now.
type runnerTargetRow struct {
	Name   string   `json:"name"`
	Labels []string `json:"labels,omitempty"`
	Online bool     `json:"online"`
}

// listRunnerTargets answers the "Machine" and "Or any machine labelled"
// dropdowns on the Run on your machine step.
//
// Gated on graph:edit rather than requireRunnerAdmin, and that difference is the
// whole reason this route exists next to the admin one. Using a runner in a flow
// already needs graph:edit and nothing more (see docs/guide/runners.md), so an
// editor who may target a machine may obviously be told which machines there
// are — while the admin endpoint stays admin-only, because it also mints
// credentials and deletes runners. Sending an editor to the admin route instead
// would have meant either a 403 on a field they are entitled to fill in, or
// widening the endpoint that hands out registration tokens.
func (h *HTTPGateway) listRunnerTargets(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !core.CanAdminOrg(p) && !p.Has(core.PermGraphEdit) {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "graph:edit required")
		return
	}
	if !h.runnersConfigured(rw) {
		return
	}
	rows, err := h.Runners.List(r.Context(), p.Tenant)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	out := make([]runnerTargetRow, 0, len(rows))
	for _, x := range rows {
		out = append(out, runnerTargetRow{
			Name:   x.Name,
			Labels: x.Labels,
			Online: x.Online(now),
		})
	}
	writeJSON(rw, http.StatusOK, map[string]any{"runners": out})
}

// mintRunnerToken returns a registration token, shown once.
//
// POST rather than GET because it creates something, and because a token in a
// URL would end up in a proxy log.
func (h *HTTPGateway) mintRunnerToken(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireRunnerAdmin(rw, p) || !h.runnersConfigured(rw) {
		return
	}
	tok, err := h.Runners.MintToken(r.Context(), p.Tenant, p.Subject)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	// Audited: this is the moment a new machine becomes able to join the org.
	h.audit(r.Context(), p, "runner.token", "", "")
	writeJSON(rw, http.StatusOK, tok)
}

// setRunnerLabelsRequest replaces the whole set. There is no add or remove
// verb: the labels are what routes work to this machine, so two admins editing
// the same one should each end with a set they meant, not a merge of both.
type setRunnerLabelsRequest struct {
	Labels []string `json:"labels"`
}

// setRunnerLabels retags a machine — which pools it belongs to — from the admin
// page, rather than only at install time via `--labels`.
//
// It exists because a label was previously decided on the machine and fixed
// there forever: putting an existing server into a new pool meant a visit to it
// (or deleting the runner, minting a token, and re-installing), for a change
// that is purely about how this Dazyflow routes work.
//
// Admin-gated and audited like registration, and deliberately not graph:edit.
// Retagging reroutes every step that targets the label — a machine can be
// pulled into, or out of, work it was never meant for without anyone touching
// a flow.
func (h *HTTPGateway) setRunnerLabels(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireRunnerAdmin(rw, p) || !h.runnersConfigured(rw) {
		return
	}
	var req setRunnerLabelsRequest
	if err := decodeRunnerBody(r, &req); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "malformed request body")
		return
	}
	name := r.PathValue("name")
	runner, err := h.Runners.SetLabels(r.Context(), p.Tenant, name, req.Labels)
	if err != nil {
		if errors.Is(err, ErrRunnerNotFound) {
			writeJSONError(rw, http.StatusNotFound, "no runner named "+name)
			return
		}
		// A rejected label is the caller's mistake and the message names which
		// one and why, so it goes back as a 400 rather than a 500.
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r.Context(), p, "runner.labels", name, strings.Join(runner.Labels, ","))
	writeJSON(rw, http.StatusOK, runnerRow{
		Name:      runner.Name,
		Labels:    runner.Labels,
		Version:   runner.Version,
		Online:    runner.Online(time.Now()),
		LastSeen:  runner.LastSeen,
		CreatedBy: runner.CreatedBy,
		CreatedAt: runner.CreatedAt,
	})
}

func (h *HTTPGateway) deleteRunner(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireRunnerAdmin(rw, p) || !h.runnersConfigured(rw) {
		return
	}
	name := r.PathValue("name")
	if err := h.Runners.Delete(r.Context(), p.Tenant, name); err != nil {
		if errors.Is(err, ErrRunnerNotFound) {
			writeJSONError(rw, http.StatusNotFound, "no runner named "+name)
			return
		}
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "runner.delete", name, "")
	writeJSON(rw, http.StatusOK, map[string]any{"deleted": name})
}

// ---- runner side ------------------------------------------------------

type registerRequest struct {
	Token   string   `json:"token"`
	Name    string   `json:"name"`
	Labels  []string `json:"labels,omitempty"`
	Version string   `json:"version,omitempty"`
}

type registerResponse struct {
	Name       string `json:"name"`
	Credential string `json:"credential"`
}

// registerRunner exchanges a registration token for a credential.
//
// Note what the request does NOT carry: a tenant. The token decides which
// organisation the runner joins. Accepting one from the caller would make a
// typo a cross-tenant registration, and the token the only thing standing
// between one org and another's work queue.
func (h *HTTPGateway) registerRunner(rw http.ResponseWriter, r *http.Request) {
	if !h.runnersConfigured(rw) {
		return
	}
	var req registerRequest
	if err := decodeRunnerBody(r, &req); err != nil {
		writeJSONError(rw, http.StatusBadRequest, "malformed request body")
		return
	}
	runner, cred, err := h.Runners.Register(r.Context(), req.Token, req.Name, req.Labels, req.Version)
	if err != nil {
		if errors.Is(err, ErrBadRunnerToken) {
			// 401, not 400: the token is a credential, and this is an
			// authentication failure. The message stays vague on purpose —
			// distinguishing expired from unknown would help someone probing.
			writeJSONError(rw, http.StatusUnauthorized, "registration token is not valid")
			return
		}
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	h.logger.Printf("runner %q registered for tenant %q", runner.Name, runner.Tenant)
	writeJSON(rw, http.StatusOK, registerResponse{Name: runner.Name, Credential: cred})
}

// authRunner identifies the agent behind a request, or writes the 401 itself.
func (h *HTTPGateway) authRunner(rw http.ResponseWriter, r *http.Request) (Runner, bool) {
	cred := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	cred = strings.TrimSpace(cred)
	if cred == "" {
		writeJSONError(rw, http.StatusUnauthorized, "runner credential required")
		return Runner{}, false
	}
	runner, err := h.Runners.Authenticate(r.Context(), cred)
	if err != nil {
		// A deleted runner lands here too, which is the intended way to revoke
		// one: the credential simply stops identifying anything.
		writeJSONError(rw, http.StatusUnauthorized, "runner credential is not valid")
		return Runner{}, false
	}
	return runner, true
}

type claimResponse struct {
	ID     string `json:"id"`
	Script string `json:"script"`
	// Shell is the interpreter to start the script with, omitted when the step
	// asked for the machine's own shell. An agent that predates the field
	// ignores it and uses the machine's shell — which is why the step's help
	// names the agent version the choice needs.
	Shell   string            `json:"shell,omitempty"`
	Stdin   string            `json:"stdin,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Timeout int64             `json:"timeout_seconds,omitempty"`
}

// claimRunnerTask hands the agent its next piece of work.
//
// 204 for "nothing to do" rather than an empty 200: it is the answer to most
// polls, and a status code the agent can branch on without parsing a body.
//
// The call doubles as the heartbeat — Authenticate records the check-in — which
// is why an idle agent must keep polling rather than sleeping quietly. That is
// also what makes "online" mean something without a connection to watch.
func (h *HTTPGateway) claimRunnerTask(rw http.ResponseWriter, r *http.Request) {
	if !h.runnerTasksConfigured(rw) {
		return
	}
	runner, ok := h.authRunner(rw, r)
	if !ok {
		return
	}
	task, err := h.RunnerTasks.Claim(r.Context(), runner, time.Now(), TaskLease)
	if errors.Is(err, ErrNoTask) {
		rw.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, claimResponse{
		ID:      task.ID,
		Script:  task.Script,
		Shell:   task.Shell,
		Stdin:   task.Stdin,
		Env:     task.Env,
		Timeout: int64(task.Timeout / time.Second),
	})
}

type progressRequest struct {
	Message string `json:"message,omitempty"`
}

// runnerTaskProgress extends the lease and forwards a line of output.
//
// Extending on progress is what lets a long script hold its task without the
// lease having to be set to the longest imaginable runtime: a script that says
// nothing for the whole lease is indistinguishable from an agent that died, and
// the honest response to that is to let the task go.
func (h *HTTPGateway) runnerTaskProgress(rw http.ResponseWriter, r *http.Request) {
	if !h.runnerTasksConfigured(rw) {
		return
	}
	runner, ok := h.authRunner(rw, r)
	if !ok {
		return
	}
	var req progressRequest
	_ = decodeRunnerBody(r, &req) // a progress ping with no body is fine
	id := r.PathValue("id")
	if err := h.RunnerTasks.Extend(r.Context(), runner, id, time.Now().Add(TaskLease), req.Message); err != nil {
		writeRunnerTaskError(rw, err)
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}

// runnerTaskResult records the outcome and releases the task.
func (h *HTTPGateway) runnerTaskResult(rw http.ResponseWriter, r *http.Request) {
	if !h.runnerTasksConfigured(rw) {
		return
	}
	runner, ok := h.authRunner(rw, r)
	if !ok {
		return
	}
	var res RunnerTaskResult
	if err := decodeRunnerBody(r, &res); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			// 413, and a message that says what to do. The old 400 read as
			// "your agent is broken" for a script that simply printed a lot,
			// and the agent treated it as terminal — so the task stranded and
			// the step blamed a machine that was online and healthy.
			writeJSONError(rw, http.StatusRequestEntityTooLarge,
				"this step's output is larger than the server accepts; "+
					"have the script write it to a file or print less")
			return
		}
		writeJSONError(rw, http.StatusBadRequest, "malformed request body")
		return
	}
	id := r.PathValue("id")
	if err := h.RunnerTasks.Complete(r.Context(), runner, id, res, time.Now()); err != nil {
		writeRunnerTaskError(rw, err)
		return
	}
	rw.WriteHeader(http.StatusNoContent)
}

// writeRunnerTaskError separates "this task is not yours" from "the database
// was briefly unhappy".
//
// The distinction is the agent's, not ours: it treats a 409 as terminal and
// moves on, so collapsing a transient pool error into one throws away a result
// that could have been retried, and the step then fails with "the runner
// stopped responding" for a machine that is online and holding the answer.
func writeRunnerTaskError(rw http.ResponseWriter, err error) {
	if errors.Is(err, ErrTaskNotClaimable) {
		// 409: the agent is reporting on work it no longer holds, which is a
		// state conflict rather than a bad request. It should stop and poll.
		writeJSONError(rw, http.StatusConflict, "this task is no longer yours")
		return
	}
	// 503 rather than 500: it is worth retrying, and the agent branches on it.
	writeJSONError(rw, http.StatusServiceUnavailable,
		"could not record this just now — try again")
}

// decodeRunnerBody reads a bounded JSON body.
//
// The cap matters more here than on most endpoints: an agent posts a script's
// entire output, and a runaway script producing gigabytes must not become the
// daemon's problem.
func decodeRunnerBody(r *http.Request, into any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, MaxRunnerBodyBytes)).Decode(into)
}

// MaxRunnerBodyBytes caps an agent's request body. Exported because the agent
// is the one that has to stay under it: it trims its own output first, so the
// step gets a clear "the script printed too much" rather than a rejected POST
// and a task nobody ever closes.
const MaxRunnerBodyBytes = 4 << 20
