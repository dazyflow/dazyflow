// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

type OnError string

const (
	OnErrorAbort    OnError = "abort"
	OnErrorSkip     OnError = "skip"
	OnErrorRetry    OnError = "retry"
	OnErrorFallback OnError = "fallback"
)

// Checked here, not only at a wire boundary: an unrecognized value falls through
// the engine's switch as abort, so a typo like "fallbcak" silently drops the
// failure handling the author asked for.
func (o OnError) Valid() bool {
	switch o {
	case "", OnErrorAbort, OnErrorSkip, OnErrorRetry, OnErrorFallback:
		return true
	}
	return false
}

const (
	MaxEdgeWaypoints  = 256
	MaxGraphWaypoints = 20000
	MaxGraphFrames    = 1000
)

// Counts the Triggers array AND trigger STEPS against ONE budget: the scheduler
// keys a step's entry by node id, so identical schedules cannot collapse the way
// array entries do, and capping only the array let the flood back in by pasting
// the step.
const MaxGraphTriggers = 32

// Measured with ApproxValueSize, which stops past maxValueDepth and reports the
// budget as spent, so a hostile graph is refused rather than accepted unmeasured.
const MaxGraphBytes = 16 << 20 // 16 MiB

const DefaultMaxVariadicFanIn = 64

// The ceiling no manifest can raise: a remote runner's manifest arrives over gRPC
// and its max is taken verbatim, so a port declaring max=1000000 would put fan-in
// back where it was before the default existed.
const MaxVariadicFanIn = 1024

type Node struct {
	ID     string            `json:"id"`
	Module string            `json:"module"`
	Params map[string]any    `json:"params"`
	Env    map[string]string `json:"env"`

	// Empty on almost every node: storing the drop's default would freeze it in ONE
	// language, the editor's fallback being localized.
	Label string `json:"label,omitempty"`

	Position *Position `json:"position,omitempty"`

	Collapsed bool `json:"collapsed,omitempty"`

	// A guard against the slip, NOT a permission: the engine runs a locked node like
	// any other and the API does not refuse writes to one.
	Locked bool `json:"locked,omitempty"`

	TimeoutSeconds int `json:"timeout_seconds,omitempty"`

	Breakpoint bool `json:"breakpoint,omitempty"`

	Disabled bool `json:"disabled,omitempty"`

	ContinueOnError bool `json:"continue_on_error,omitempty"`
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Edge struct {
	From      string     `json:"from"`
	FromPort  string     `json:"from_port"`
	To        string     `json:"to"`
	ToPort    string     `json:"to_port"`
	OnError   OnError    `json:"on_error"`
	Waypoints []Position `json:"waypoints,omitempty"`
}

type Frame struct {
	ID     string  `json:"id"`
	Title  string  `json:"title,omitempty"`
	Color  string  `json:"color,omitempty"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type Graph struct {
	ID        string         `json:"id"`
	Version   string         `json:"version"`
	Tenant    string         `json:"tenant"`
	Workspace string         `json:"workspace"`
	Nodes     []Node         `json:"nodes"`
	Edges     []Edge         `json:"edges"`
	Triggers  []GraphTrigger `json:"triggers,omitempty"`
	Frames    []Frame        `json:"frames,omitempty"`

	Name        string `json:"name,omitempty"`
	Icon        string `json:"icon,omitempty"`
	Description string `json:"description,omitempty"`

	Visibility Visibility `json:"visibility,omitempty"`

	Owner string `json:"owner,omitempty"`

	FailureNotify *FailureNotify `json:"failure_notify,omitempty"`

	TimeoutSeconds int `json:"timeout_seconds,omitempty"`

	Language string `json:"language,omitempty"`

	Disabled bool `json:"disabled,omitempty"`

	ContinueOnError bool `json:"continue_on_error,omitempty"`
}

// Stored as-is in the workspace Git repo, so additions are backwards-compatible
// — but the daemon's visibility checks must handle each one explicitly.
type Visibility string

const (
	VisibilityOrg     Visibility = "org"
	VisibilityPrivate Visibility = "private"
)

// Consult this anywhere the on-disk record is read, never the raw field.
func (g Graph) EffectiveVisibility() Visibility {
	if g.Visibility == VisibilityPrivate {
		return VisibilityPrivate
	}
	return VisibilityOrg
}

// Poll anchors its interval to the last fire, not to wall-clock boundaries: a
// 300-second trigger started at 09:01:23 fires at 09:06:23.
type GraphTrigger struct {
	Type string `json:"type"`           // "cron", "webhook", or "poll"
	Cron string `json:"cron,omitempty"` // for type=cron
	// Anchors the cron's wall-clock fields to a real zone, so "0 9 * * *" survives
	// DST rather than tracking the daemon host's local time. Empty means UTC.
	TZ              string   `json:"tz,omitempty"`
	Secret          string   `json:"secret,omitempty"`           // for type=webhook (compared against Authorization header)
	IntervalSeconds int      `json:"interval_seconds,omitempty"` // for type=poll; must be > 0
	PublicForm      bool     `json:"public_form,omitempty"`
	FormFields      []string `json:"form_fields,omitempty"`
	FormTitle       string   `json:"form_title,omitempty"`
}

type FailureNotify struct {
	Webhook string `json:"webhook,omitempty"`
	Email   string `json:"email,omitempty"`
}

func (g Graph) Node(id string) (Node, bool) {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

func (g Graph) UpstreamSubset(target string) (Graph, bool) {
	if _, ok := g.Node(target); !ok {
		return Graph{}, false
	}
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
