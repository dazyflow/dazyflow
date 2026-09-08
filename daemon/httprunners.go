// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

type runnerAPI struct {
	auditor
	urlBuilder
	svc         *Service
	logger      *log.Logger
	Runners     *Runners
	RunnerTasks RunnerTaskStore
}

func (h *HTTPGateway) runnerAPI() *runnerAPI {
	return &runnerAPI{auditor: h.auditor(), urlBuilder: h.urls(), svc: h.svc, logger: h.logger, Runners: h.Runners, RunnerTasks: h.RunnerTasks}
}

// Two audiences and two credentials that must not be confused: an admin holds a
// session, an agent holds a runner key. The agent's endpoints therefore sit
// outside requireAuth.

// No credential in it, and none is ever returned by this API.
type runnerRow struct {
	Name      string    `json:"name"`
	Labels    []string  `json:"labels,omitempty"`
	Version   string    `json:"version,omitempty"`
	Online    bool      `json:"online"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Adding a source of executable steps is a bigger power than editing a flow, so
// it is gated above graph:edit.
func requireStepSourceAdmin(rw http.ResponseWriter, p core.Principal) bool {
	if core.CanAdminOrg(p) || p.Has(core.PermModuleRegister) {
		return true
	}
	writeAPIError(rw, http.StatusForbidden, "forbidden",
		"organization:admin or module:register required")
	return false
}

func (h *runnerAPI) runnersConfigured(rw http.ResponseWriter) bool {
	if h.Runners == nil || h.Runners.Store == nil {
		writeJSONError(rw, http.StatusNotImplemented, "runners are not configured on this deployment")
		return false
	}
	return true
}

func (h *runnerAPI) runnerTasksConfigured(rw http.ResponseWriter) bool {
	if !h.runnersConfigured(rw) {
		return false
	}
	if h.RunnerTasks == nil {
		writeJSONError(rw, http.StatusNotImplemented, "runners are not configured on this deployment")
		return false
	}
	return true
}

func (h *runnerAPI) listRunners(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.runnersConfigured(rw) {
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

type runnerTargetRow struct {
	Name   string   `json:"name"`
	Tags   []string `json:"tags,omitempty"`
	Online bool     `json:"online"`
}

// Every tag NARROWS the set, so the count tells the author what a tag matches.
func (h *runnerAPI) listRunnerTargets(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
			Tags:   x.Tags(),
			Online: x.Online(now),
		})
	}
	writeJSON(rw, http.StatusOK, map[string]any{"runners": out})
}

type mintTokenRequest struct {
	Name string `json:"name,omitempty"`
}

func (h *runnerAPI) mintRunnerToken(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.runnersConfigured(rw) {
		return
	}
	var req mintTokenRequest
	if err := decodeRunnerBody(r, &req); err != nil && !errors.Is(err, io.EOF) {
		writeJSONError(rw, http.StatusBadRequest, "malformed request body")
		return
	}
	if req.Name != "" {
		if err := validRunnerName(req.Name); err != nil {
			writeAPIError(rw, http.StatusBadRequest, "invalid_name", "runner name: "+err.Error())
			return
		}
	}
	tok, err := h.Runners.MintToken(r.Context(), p.Tenant, p.Subject, req.Name)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r.Context(), p, "runner.token", req.Name, "")
	writeJSON(rw, http.StatusOK, tok)
}

type setRunnerLabelsRequest struct {
	Labels []string `json:"labels"`
}

func (h *runnerAPI) setRunnerLabels(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.runnersConfigured(rw) {
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

func (h *runnerAPI) deleteRunner(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !requireStepSourceAdmin(rw, p) || !h.runnersConfigured(rw) {
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

func (h *runnerAPI) registerRunner(rw http.ResponseWriter, r *http.Request) {
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
		switch {
		case errors.Is(err, ErrBadRunnerToken):
			writeJSONError(rw, http.StatusUnauthorized, "registration token is not valid")
		case errors.Is(err, ErrRunnerNameTaken):
			writeJSONError(rw, http.StatusConflict,
				"a runner with this name already exists; choose another name, "+
					"or have an admin mint a token for this name to replace it")
		case errors.Is(err, ErrRunnerNameMismatch):
			writeJSONError(rw, http.StatusForbidden,
				"this registration token is for a different runner name")
		default:
			writeJSONError(rw, http.StatusBadRequest, err.Error())
		}
		return
	}
	h.logger.Printf("runner %q registered for tenant %q", runner.Name, runner.Tenant)
	writeJSON(rw, http.StatusOK, registerResponse{Name: runner.Name, Credential: cred})
}

func (h *runnerAPI) authRunner(rw http.ResponseWriter, r *http.Request) (Runner, bool) {
	cred := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	cred = strings.TrimSpace(cred)
	if cred == "" {
		writeJSONError(rw, http.StatusUnauthorized, "runner credential required")
		return Runner{}, false
	}
	runner, err := h.Runners.Authenticate(r.Context(), cred)
	if err != nil {
		writeJSONError(rw, http.StatusUnauthorized, "runner credential is not valid")
		return Runner{}, false
	}
	return runner, true
}

type claimResponse struct {
	ID      string            `json:"id"`
	Script  string            `json:"script"`
	Shell   string            `json:"shell,omitempty"`
	Stdin   string            `json:"stdin,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Timeout int64             `json:"timeout_seconds,omitempty"`
}

func (h *runnerAPI) claimRunnerTask(rw http.ResponseWriter, r *http.Request) {
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

func (h *runnerAPI) runnerTaskProgress(rw http.ResponseWriter, r *http.Request) {
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

func (h *runnerAPI) runnerTaskResult(rw http.ResponseWriter, r *http.Request) {
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

func writeRunnerTaskError(rw http.ResponseWriter, err error) {
	if errors.Is(err, ErrTaskNotClaimable) {
		writeJSONError(rw, http.StatusConflict, "this task is no longer yours")
		return
	}
	writeJSONError(rw, http.StatusServiceUnavailable,
		"could not record this just now — try again")
}

func decodeRunnerBody(r *http.Request, into any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, MaxRunnerBodyBytes)).Decode(into)
}

const MaxRunnerBodyBytes = 4 << 20
