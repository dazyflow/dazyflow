package core

type OnError string

const (
	OnErrorAbort    OnError = "abort"
	OnErrorSkip     OnError = "skip"
	OnErrorRetry    OnError = "retry"
	OnErrorFallback OnError = "fallback"
)

type Node struct {
	ID     string            `json:"id"`
	Module string            `json:"module"`
	Params map[string]any    `json:"params"`
	Env    map[string]string `json:"env"`

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
}

type Graph struct {
	ID        string         `json:"id"`
	Version   string         `json:"version"`
	Tenant    string         `json:"tenant"`
	Workspace string         `json:"workspace"`
	Nodes     []Node         `json:"nodes"`
	Edges     []Edge         `json:"edges"`
	Triggers  []GraphTrigger `json:"triggers,omitempty"`

	// Display metadata. None of these affect engine behaviour — they
	// are surfaces the UI shows in the flow list, switcher, and
	// settings panel. ID remains the immutable handle for runs and
	// trigger URLs.
	Name        string `json:"name,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Description string `json:"description,omitempty"`

	// Visibility controls who in the workspace can see/run this flow:
	//   - "org" (default): any principal in the tenant+workspace
	//   - "private":       only the Owner and tenant:admin principals
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
	// The hzd `-default-graph-timeout` flag is applied at SubmitGraph
	// time when this field is unset.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
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
//	{"type": "cron",    "cron": "0 9 * * *"}              — daily 09:00 (workspace tz)
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
	Type            string `json:"type"`                       // "cron", "webhook", or "poll"
	Cron            string `json:"cron,omitempty"`             // for type=cron
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
	sub := g
	sub.Nodes = make([]Node, 0, len(included))
	for _, n := range g.Nodes {
		if included[n.ID] {
			sub.Nodes = append(sub.Nodes, n)
		}
	}
	sub.Edges = make([]Edge, 0, len(g.Edges))
	for _, e := range g.Edges {
		if included[e.From] && included[e.To] {
			sub.Edges = append(sub.Edges, e)
		}
	}
	return sub, true
}
