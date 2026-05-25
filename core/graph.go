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
// Currently two types are supported:
//
//	{"type": "cron", "cron": "0 9 * * *"}      — daily 09:00 (workspace tz)
//	{"type": "webhook", "secret": "<token>"}   — POST /trigger/<tenant>/<workspace>/<graph>
//
// Multiple triggers can coexist on the same graph (e.g. a graph that
// runs hourly AND can be manually triggered via webhook).
type GraphTrigger struct {
	Type   string `json:"type"`             // "cron" or "webhook"
	Cron   string `json:"cron,omitempty"`   // for type=cron
	Secret string `json:"secret,omitempty"` // for type=webhook (compared against Authorization header)
}

func (g Graph) Node(id string) (Node, bool) {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}
