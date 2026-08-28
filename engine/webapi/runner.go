// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package webapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// The last mile, for a service that is not on the internet.
//
// Dazyflow refuses to dial private addresses (hfnet.SSRFDialControl) and always
// will, so an org's internal orders API cannot be reached by the daemon at all —
// the one gap phase 1 left open. A runner already sits inside that network and
// already asks the daemon for work, so the request is rendered as a small
// script, queued as an ordinary runner task, and performed from in there.
//
// Everything above this file is unchanged: same descriptor, same synthesized
// manifests, same ports, same connection injection. buildRequest still assembles
// method/url/headers/body exactly as it does for a direct call. ONLY the final
// hop swaps, which is why this is one field on the catalog rather than a second
// product.

// Dispatcher runs one script on a machine carrying all of Tags and returns what
// it printed. The shape is drops/runner's Dispatcher, narrowed to what this
// package needs, so cmd/dzd can bridge the two with a struct copy.
//
// INJECTED for the same reason Doer is: drops/runner imports engine, and
// engine/webapi importing it would be a cycle. cmd/dzd is the only place that
// legitimately knows both sides.
type Dispatcher interface {
	Dispatch(ctx context.Context, req RunnerRequest, onProgress func(string)) (RunnerResult, error)
}

// RunnerRequest is one script to run. Mirrors drops/runner.Request field for
// field; the bridge in cmd/dzd copies between them.
type RunnerRequest struct {
	Tenant  string
	Tags    []string
	Script  string
	Shell   string
	Stdin   string
	Timeout time.Duration
}

// RunnerResult is what came back off the machine.
type RunnerResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Error    string
}

var dispatcherHook atomic.Pointer[Dispatcher]

// SetDispatcher installs the runner dispatcher. Passing nil clears it.
//
// Unset is NOT fatal the way an unset Doer is: a deployment with no runners
// still runs every direct-call catalog perfectly well. It only fails the
// catalogs that asked to be reached through one, and it says so naming the
// catalog rather than failing as a generic transport error.
func SetDispatcher(d Dispatcher) {
	if d == nil {
		dispatcherHook.Store(nil)
		return
	}
	dispatcherHook.Store(&d)
}

func currentDispatcher() (Dispatcher, bool) {
	if p := dispatcherHook.Load(); p != nil && *p != nil {
		return *p, true
	}
	return nil, false
}

// runnerShell is the interpreter the request script is written for.
//
// Python, and it is not a toss-up: the runner agent IS python3
// (runner/dzrunner.py — "if you have python3, you have the agent"), and its
// interpreter_argv falls back to sys.executable for this shell, so the one
// interpreter guaranteed to exist on a machine running a runner is this one. A
// shell script would need curl, which is not guaranteed anywhere and is absent
// by default on Windows.
//
// The script uses only the standard library, matching the agent's own "no
// dependency to vet" rule.
const runnerShell = "python"

// requestScript performs one HTTP call from inside the org's network.
//
// It carries NO request detail of its own: method, URL, headers and body all
// arrive on stdin as one JSON envelope, so this text is a constant. That is
// deliberate and is the security-relevant half of this file — the credential
// travels in the auth header, and runner_tasks persists `script` in plaintext
// alongside `stdin`. Keeping the script constant means the column that is read
// while debugging a queue never holds a token, and there is exactly one place
// (stdin) to reason about instead of two.
//
// The design note said "the body on Stdin"; putting the whole envelope there is
// the same idea carried one field further, for that reason.
//
// It prints one JSON object on stdout and nothing else, so the parse below is
// total rather than a scrape. Body is base64 so a binary response survives the
// trip — stdout is text, and a PDF through it would not.
const requestScript = `import base64, json, sys, urllib.error, urllib.request

req = json.load(sys.stdin)
body = base64.b64decode(req["body"]) if req.get("body") else None
r = urllib.request.Request(req["url"], data=body, method=req["method"])
for k, v in (req.get("headers") or {}).items():
    r.add_header(k, v)

try:
    with urllib.request.urlopen(r, timeout=req["timeout_s"]) as resp:
        status, headers, payload = resp.status, resp.headers, resp.read(req["max_bytes"] + 1)
except urllib.error.HTTPError as e:
    # A 4xx/5xx is an ANSWER, not a transport failure: the step's expect_status
    # decides whether it is acceptable, exactly as it does for a direct call.
    with e:
        status, headers, payload = e.code, e.headers, e.read(req["max_bytes"] + 1)
except Exception as e:
    json.dump({"error": "%s: %s" % (type(e).__name__, e)}, sys.stdout)
    sys.exit(0)

if len(payload) > req["max_bytes"]:
    json.dump({"error": "response larger than the %d byte cap" % req["max_bytes"]}, sys.stdout)
    sys.exit(0)

json.dump({
    "status": status,
    "headers": {k: v for k, v in headers.items()},
    "body_b64": base64.b64encode(payload).decode("ascii"),
}, sys.stdout)
`

// runnerEnvelope is what the script reads off stdin.
type runnerEnvelope struct {
	Method   string            `json:"method"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	Body     string            `json:"body,omitempty"` // base64
	TimeoutS float64           `json:"timeout_s"`
	MaxBytes int               `json:"max_bytes"`
}

// runnerReply is what it prints. Exactly one of Error or a status is set.
type runnerReply struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	BodyB64 string            `json:"body_b64"`
	Error   string            `json:"error"`
}

// viaRunner performs req on a machine carrying the catalog's tags.
//
// The signature deliberately matches the Doer call in Execute — (status, body,
// headers, error) — so the branch in Execute is the choice of transport and
// nothing else. Everything downstream (expect_status, MIME sniffing, the three
// output ports) is shared.
func (t *Transport) viaRunner(
	ctx context.Context,
	req request,
	timeoutMS, maxBytes int,
	progress func(string),
) (int, []byte, map[string]string, error) {
	disp, ok := currentDispatcher()
	if !ok {
		return 0, nil, nil, fmt.Errorf(
			"catalog %q is set to be reached through a runner, but this deployment has no runner dispatcher wired",
			t.desc.Name)
	}

	envelope, err := json.Marshal(runnerEnvelope{
		Method:   req.method,
		URL:      req.url,
		Headers:  req.headers,
		Body:     b64(req.body),
		TimeoutS: float64(timeoutMS) / 1000,
		MaxBytes: maxBytes,
	})
	if err != nil {
		return 0, nil, nil, fmt.Errorf("could not encode the request for the runner: %w", err)
	}

	// The runner's own deadline sits ABOVE the request's: the script is given
	// the HTTP timeout, and the task is given that plus a margin to start
	// python, read stdin and print. Equal values would race, and the loser is
	// the more useful message — "the runner stopped responding" instead of the
	// service's own timeout.
	taskTimeout := time.Duration(timeoutMS)*time.Millisecond + runnerOverhead

	res, err := disp.Dispatch(ctx, RunnerRequest{
		Tenant:  t.desc.Tenant,
		Tags:    t.desc.Runner.Tags,
		Script:  requestScript,
		Shell:   runnerShell,
		Stdin:   string(envelope),
		Timeout: taskTimeout,
	}, progress)
	if err != nil {
		return 0, nil, nil, err
	}
	return parseRunnerReply(res)
}

// runnerOverhead is the margin between the HTTP timeout and the task timeout —
// long enough to start an interpreter on a busy machine, short enough that a
// wedged task is not held open for minutes after the call it was making gave up.
const runnerOverhead = 30 * time.Second

// parseRunnerReply turns what the machine printed into the same four values a
// direct call produces.
func parseRunnerReply(res RunnerResult) (int, []byte, map[string]string, error) {
	// The dispatcher's own failure — nothing matched the tags, the machine went
	// away mid-task. Its message already names the runner, so it is passed
	// through rather than re-derived.
	if res.Error != "" {
		return 0, nil, nil, fmt.Errorf("runner: %s", res.Error)
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		// The interpreter never got as far as printing. Overwhelmingly this is
		// a runner started with --allow that does not permit python, so stderr
		// is the only thing that explains it and it is quoted rather than
		// summarised.
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = fmt.Sprintf("no output (exit code %d)", res.ExitCode)
		}
		return 0, nil, nil, fmt.Errorf("the runner ran the request but printed nothing: %s", msg)
	}

	var reply runnerReply
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		return 0, nil, nil, fmt.Errorf("the runner printed something this step could not read: %s", bodySnippet([]byte(out)))
	}
	if reply.Error != "" {
		// The call itself failed inside the network — DNS, connection refused,
		// the service's own timeout. Not a transport fault of ours.
		return 0, nil, nil, fmt.Errorf("%s", reply.Error)
	}
	body, err := unb64(reply.BodyB64)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("the runner's response body was not readable: %w", err)
	}
	return reply.Status, body, reply.Headers, nil
}
