// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

type OnError string

const (
	OnErrorAbort    OnError = "abort"
	OnErrorSkip     OnError = "skip"
	OnErrorRetry    OnError = "retry"
	OnErrorFallback OnError = "fallback"
)

// Valid reports whether o is one of the four defined policies. Empty is
// valid and means "unset" — the engine's default abort behaviour.
//
// This is checked in Validate rather than only at a wire boundary because
// OnError arrives as a free string from every entry path (proto conversion
// casts it, JSON unmarshals it, MCP passes it through). An unrecognized
// value used to be accepted silently and then fall through the engine's
// switch, so a typo like "fallbcak" quietly downgraded a fallback edge to
// abort — the failure-handling the author asked for simply didn't happen,
// with nothing anywhere reporting it.
func (o OnError) Valid() bool {
	switch o {
	case "", OnErrorAbort, OnErrorSkip, OnErrorRetry, OnErrorFallback:
		return true
	}
	return false
}

type Node struct {
	ID     string            `json:"id"`
	Module string            `json:"module"`
	Params map[string]any    `json:"params"`
	Env    map[string]string `json:"env"`

	// Label is the step's display name on the canvas, when the author has
	// given it one. Editor metadata, like Position: the engine never reads it,
	// and a node without one is named after its drop (the manifest's Label) —
	// which is why this is empty on almost every node rather than carrying a
	// copy of the default. Storing the default would also freeze it in ONE
	// language, since the editor's fallback is localized.
	Label string `json:"label,omitempty"`

	// Position is layout metadata for the visual editor — ignored by
	// the engine. Optional; nil-position nodes are auto-laid-out by
	// the UI on first open.
	Position *Position `json:"position,omitempty"`

	// TimeoutSeconds bounds the per-execution wall-time of this node.
	// On expiry the worker marks the node Failed with code=timeout —
	// existing failure-propagation (on_error / fallback edges) then
	// applies. Zero / unset = no per-node timeout; the graph-level
	// TimeoutSeconds (if any) still applies.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`

	// Breakpoint, when set, pauses the run after this node completes and
	// before its dependents are dispatched — a debugging aid so the
	// operator can inspect this node's output, then Continue or Step.
	// Honored by the dispatcher; see daemon/breakpoint.go.
	Breakpoint bool `json:"breakpoint,omitempty"`

	// Disabled switches the node off: at run time it is recorded as
	// skipped without executing, and the standard skip cascade then skips
	// its dependents too — "off" prunes the whole branch. A setup-time
	// aid (e.g. don't actually send emails while testing the rest of a
	// flow). Honored by the worker; see daemon/worker.go.
	Disabled bool `json:"disabled,omitempty"`

	// ContinueOnError marks this step as non-critical: if it fails, the run
	// carries on and finishes with its other branches instead of being
	// marked failed. The step's own dependents are still skipped (there is
	// no output to feed them) — the same skip cascade a disabled node
	// causes.
	//
	// It exists for the "announce it everywhere" shape: several independent
	// notifications hang off one source, and Discord being down is no reason
	// for the Slack post and the email not to go out. Without it, a terminal
	// step (one with no outgoing edges) always propagates its failure — the
	// on_error policies live on EDGES, so a step at the end of a branch has
	// nowhere to hang one.
	ContinueOnError bool `json:"continue_on_error,omitempty"`
}

// Position is a canvas X/Y coordinate. Pixels in the UI's coordinate
// system; semantics are entirely up to the editor.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Edge struct {
	From     string  `json:"from"`
	FromPort string  `json:"from_port"`
	To       string  `json:"to"`
	ToPort   string  `json:"to_port"`
	OnError  OnError `json:"on_error"`
	// Waypoints are optional editor-only routing knots: bend points the
	// wire is drawn through. The engine ignores them entirely; they exist
	// so the editor can redraw the same hand-tuned routing. Stored inline
	// in the graph JSON, so no schema change is needed.
	Waypoints []Position `json:"waypoints,omitempty"`
}

// Frame is an editor-only comment box that visually groups nodes on the
// canvas. The engine ignores frames entirely; they round-trip in the
// graph JSON so the editor can redraw them. Position/size are in the
// editor's flow coordinate system.
type Frame struct {
	ID     string  `json:"id"`
	Title  string  `json:"title,omitempty"`
	Color  string  `json:"color,omitempty"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// legacyHeadersPorts are the port names that carried column order as a separate
// wire before it was folded onto the row value (core.Ref.Headers). The ports
// are removed from manifests, so any saved/authored graph still wiring them
// would fail port validation — MigrateGraph drops those now-dangling edges.
var legacyHeadersPorts = map[string]bool{"headers": true, "left_headers": true, "right_headers": true}

// MigrateGraph brings a stored or submitted graph up to the current data model.
// Today that means dropping edges to/from the folded-away `headers` ports: the
// column order now travels on the row value, so a headers edge is obsolete and
// (with the port gone) otherwise invalid. Idempotent and cheap — safe to run on
// every load. Returns the same graph value with Edges filtered.
func MigrateGraph(g Graph) Graph {
	if len(g.Edges) == 0 {
		return g
	}
	kept := g.Edges[:0:0]
	for _, e := range g.Edges {
		if legacyHeadersPorts[e.FromPort] || legacyHeadersPorts[e.ToPort] {
			continue // obsolete headers wire — column order rides on the row value now
		}
		kept = append(kept, e)
	}
	g.Edges = kept
	return g
}

type Graph struct {
	ID        string         `json:"id"`
	Version   string         `json:"version"`
	Tenant    string         `json:"tenant"`
	Workspace string         `json:"workspace"`
	Nodes     []Node         `json:"nodes"`
	Edges     []Edge         `json:"edges"`
	Triggers  []GraphTrigger `json:"triggers,omitempty"`
	// Frames are editor-only comment boxes — see Frame. Engine-ignored.
	Frames []Frame `json:"frames,omitempty"`

	// Display metadata. None of these affect engine behaviour — they
	// are surfaces the UI shows in the flow list, switcher, and
	// settings panel. ID remains the immutable handle for runs and
	// trigger URLs.
	Name        string `json:"name,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Description string `json:"description,omitempty"`

	// Visibility controls who in the workspace can see/run this flow:
	//   - "org" (default): any principal in the tenant+workspace
	//   - "private":       only the Owner and organization:admin principals
	// An empty value reads as "org" — keeps pre-visibility graphs
	// behaving the way they always have.
	Visibility Visibility `json:"visibility,omitempty"`

	// Owner is the subject of the principal who created the flow. Set
	// automatically by the daemon on first save and immutable through
	// the normal save path. Empty for legacy flows; visibility checks
	// treat an empty Owner as "no private-mode owner exists" which
	// effectively forces org mode regardless of Visibility.
	Owner string `json:"owner,omitempty"`

	// FailureNotify, when set, configures where the daemon sends a
	// notification when a run of this graph terminates with
	// status=failed. Webhook is the only delivery channel today —
	// works with Slack incoming-webhook URLs, Discord, PagerDuty
	// events API, generic receivers, anything that takes an HTTP
	// POST. Per-channel typed UX (Slack-channel picker, etc.) lands
	// when there's a clean per-tenant-token plumbing path; until
	// then the webhook URL covers every case via the
	// service-provided incoming-webhook surfaces.
	FailureNotify *FailureNotify `json:"failure_notify,omitempty"`

	// TimeoutSeconds caps the wall-time of any run of this graph. When
	// elapsed, the daemon auto-cancels the run via the same path as a
	// manual cancel — already-running nodes finish naturally, but no
	// further downstream work is dispatched. Zero / unset = no cap.
	// The dzd `-default-graph-timeout` flag is applied at SubmitGraph
	// time when this field is unset.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`

	// Disabled, when true, suspends all automatic firing of this flow:
	// the scheduler skips cron + poll triggers, and webhook + form
	// endpoints reject inbound calls with 403 + code "flow_disabled".
	// Manual runs via /me/flows/{id}/run and test_trigger_flow still
	// work — those paths are explicit intent ("yes, run this now")
	// rather than passive firing. Pair with the enable_flow /
	// disable_flow MCP tools to pause a flow without deleting it.
	Disabled bool `json:"disabled,omitempty"`

	// ContinueOnError marks this step as non-critical: if it fails, the run
	// carries on and finishes with its other branches instead of being
	// marked failed. The step's own dependents are still skipped (there is
	// no output to feed them) — the same skip cascade a disabled node
	// causes.
	//
	// It exists for the "announce it everywhere" shape: several independent
	// notifications hang off one source, and Discord being down is no reason
	// for the Slack post and the email not to go out. Without it, a terminal
	// step (one with no outgoing edges) always propagates its failure — the
	// on_error policies live on EDGES, so a step at the end of a branch has
	// nowhere to hang one.
	ContinueOnError bool `json:"continue_on_error,omitempty"`
}

// Visibility enumerates the access modes a flow can have. Values are
// stored as-is in the workspace Git repo, so additions are backwards-
// compatible — but the daemon's visibility checks must explicitly
// handle each value.
type Visibility string

const (
	VisibilityOrg     Visibility = "org"
	VisibilityPrivate Visibility = "private"
)

// EffectiveVisibility resolves the empty / missing value to the default.
// Use this anywhere the on-disk record is consulted; never the raw
// Visibility field.
func (g Graph) EffectiveVisibility() Visibility {
	if g.Visibility == VisibilityPrivate {
		return VisibilityPrivate
	}
	return VisibilityOrg
}

// GraphTrigger describes when the graph should fire automatically.
// Currently three types are supported:
//
//	{"type": "cron", "cron": "0 9 * * *", "tz": "Europe/Stockholm"}  — daily 09:00 in that zone
//	{"type": "webhook", "secret": "<token>"}              — POST /trigger/<tenant>/<workspace>/<graph>
//	{"type": "poll",    "interval_seconds": 300}          — fire every 5 minutes (interval-anchored)
//
// Poll differs from cron in two ways: (a) the interval is anchored
// to the last fire, not to wall-clock boundaries (a 300-second
// poll trigger started at 09:01:23 fires at 09:06:23, not 09:05:00);
// (b) it's expressed as a simple integer interval rather than a
// 5-field cron expression — purpose-built for "check for new data
// every N minutes" semantics where exact wall-clock timing isn't
// what you want.
//
// Multiple triggers can coexist on the same graph (e.g. a graph
// that runs hourly AND can be manually triggered via webhook).
type GraphTrigger struct {
	Type string `json:"type"`           // "cron", "webhook", or "poll"
	Cron string `json:"cron,omitempty"` // for type=cron
	// TZ is the IANA timezone (e.g. "Europe/Stockholm") the cron
	// expression is interpreted in, for type=cron. It anchors the
	// wall-clock fields to a real zone so "0 9 * * *" means 09:00 in
	// the user's own timezone — and survives DST — rather than the
	// daemon host's local time. The web UI stamps the editor's browser
	// timezone here on every schedule edit, so a user only ever reasons
	// about their own clock. Empty falls back to UTC (deterministic and
	// host-independent), which is also what pre-tz graphs get.
	TZ              string `json:"tz,omitempty"`
	Secret          string `json:"secret,omitempty"`           // for type=webhook (compared against Authorization header)
	IntervalSeconds int    `json:"interval_seconds,omitempty"` // for type=poll; must be > 0
	// PublicForm, on a webhook trigger, opts the graph into a hosted
	// intake form at /form/<tenant>/<workspace>/<id>. Visitors submit
	// without any bearer token — possession of the URL is the
	// capability — so this is strictly opt-in. Off by default: a normal
	// webhook trigger exposes no public page.
	PublicForm bool `json:"public_form,omitempty"`
	// FormFields names the fields the hosted form renders, in order.
	// Empty falls back to a sensible contact-form default
	// (name/email/message). Field names become the keys of the JSON
	// object delivered to webhook_input's body port.
	FormFields []string `json:"form_fields,omitempty"`
	// FormTitle is the heading shown on the hosted form. Empty falls
	// back to the graph's display name.
	FormTitle string `json:"form_title,omitempty"`
}

// FailureNotify configures where the daemon sends a notification
// when a graph run terminates with status=failed. Today only the
// webhook channel is supported; the typed schema leaves room for
// {email, slack, ...} variants without breaking older graph JSON.
//
// Payload shape (POSTed as application/json):
//
//	{
//	  "graph_id":     "my-flow",
//	  "run_id":       "run-abc",
//	  "tenant":       "acme",
//	  "workspace":    "main",
//	  "error_code":   "timeout",
//	  "error_message": "node 'enrich' exceeded 30s",
//	  "failed_node":  "enrich",
//	  "run_url":      "https://app.example.com/runs/run-abc",  // when PublicBaseURL is set
//	  "finished_at":  "2026-05-26T14:23:01Z"
//	}
type FailureNotify struct {
	// Webhook is the URL to POST the failure payload to. Empty means
	// "no notification" — same as not setting FailureNotify at all.
	Webhook string `json:"webhook,omitempty"`
	// Email, when set, receives a plain-text failure summary through
	// the operator's transactional mailer (DAZYFLOW_SMTP_URL). Ignored
	// (logged) on deployments without a mailer.
	Email string `json:"email,omitempty"`
}

func (g Graph) Node(id string) (Node, bool) {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

// UpstreamSubset returns a copy of g containing only target plus every
// node that target transitively depends on (BFS over edges in reverse).
// Edges between included nodes are preserved verbatim, including
// on_error semantics — the subset must run with the same dispatch
// rules as the full graph or "sample this node" would behave
// differently from "fire the whole graph and look at this output."
//
// Used by the sample-node endpoint to fire a partial run that ends at
// the target. Edges leaving included nodes toward excluded nodes are
// dropped — the dispatcher then sees no downstream and finalizes the
// run once target completes.
//
// Returns (Graph{}, false) when target isn't in g. Other graph fields
// (Tenant, Workspace, ID, Visibility, Owner, Triggers, FailureNotify,
// display metadata) are copied unchanged so the submitted run carries
// the same identity and authz context as the source.
func (g Graph) UpstreamSubset(target string) (Graph, bool) {
	if _, ok := g.Node(target); !ok {
		return Graph{}, false
	}
	// Reverse adjacency: dst → []src. Built once so the BFS is O(E)
	// rather than O(N·E).
	predecessors := make(map[string][]string, len(g.Nodes))
	for _, e := range g.Edges {
		predecessors[e.To] = append(predecessors[e.To], e.From)
	}
	included := map[string]bool{target: true}
	queue := []string{target}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, pred := range predecessors[cur] {
			if included[pred] {
				continue
			}
			included[pred] = true
			queue = append(queue, pred)
		}
	}
	keep := make(map[string]struct{}, len(included))
	for id := range included {
		keep[id] = struct{}{}
	}
	return g.Subset(keep), true
}

// Subset returns a copy of g restricted to the nodes whose IDs are in keep:
// nodes not in keep are dropped, and an edge survives only when BOTH of its
// endpoints are kept (so edges leaving the subset toward an excluded node are
// pruned). All other graph fields (identity, triggers, display metadata, …)
// are copied unchanged. Pure — g is not mutated.
func (g Graph) Subset(keep map[string]struct{}) Graph {
	sub := g
	sub.Nodes = make([]Node, 0, len(keep))
	for _, n := range g.Nodes {
		if _, ok := keep[n.ID]; ok {
			sub.Nodes = append(sub.Nodes, n)
		}
	}
	sub.Edges = make([]Edge, 0, len(g.Edges))
	for _, e := range g.Edges {
		_, fromOK := keep[e.From]
		_, toOK := keep[e.To]
		if fromOK && toOK {
			sub.Edges = append(sub.Edges, e)
		}
	}
	return sub
}
