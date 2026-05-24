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
