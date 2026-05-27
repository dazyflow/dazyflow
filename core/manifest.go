package core

import "encoding/json"

type ExecutionModel string

const (
	ExecutionBatch   ExecutionModel = "batch"
	ExecutionStream  ExecutionModel = "stream"
	ExecutionTrigger ExecutionModel = "trigger"
)

type ProcessModel string

const (
	ProcessSpawnPerJob   ProcessModel = "spawn_per_job"
	ProcessLongLived     ProcessModel = "long_lived"
	ProcessPreRegistered ProcessModel = "pre_registered"
)

type RetryPolicy string

const (
	RetryNever              RetryPolicy = "never"
	RetryExponentialBackoff RetryPolicy = "exponential_backoff"
)

type Port struct {
	Port     string   `json:"port"`
	MIME     []string `json:"mime"`
	Label    string   `json:"label"`
	Required bool     `json:"required"`
	Variadic bool     `json:"variadic"`
	Min      *int     `json:"min,omitempty"`
	Max      *int     `json:"max,omitempty"`
}

type Manifest struct {
	ID             string          `json:"id"`
	Version        string          `json:"version"`
	Label          string          `json:"label"`
	Color          string          `json:"color"`
	ExecutionModel ExecutionModel  `json:"execution_model"`
	ProcessModel   ProcessModel    `json:"process_model"`
	Inputs         []Port          `json:"inputs"`
	Outputs        []Port          `json:"outputs"`
	ParamsSchema   json.RawMessage `json:"params_schema"`
	Idempotent     bool            `json:"idempotent"`
	RetryPolicy    RetryPolicy     `json:"retry_policy"`

	// MaxRetries lets a module author override the worker-global retry
	// cap (total attempts) for nodes running this module. Zero means
	// "unspecified — use the worker default". A flaky network module can
	// set it high ("tolerates 10"); a costly or one-shot module can set
	// it to 1 (a single attempt, no retry). Only consulted when
	// RetryPolicy already opts the module into retries.
	MaxRetries int `json:"max_retries,omitempty"`

	CompatibleWith []string `json:"compatible_with"`

	// --- Discovery metadata (introduced for search + categorization) ---

	// Category is a single-bucket classification. Recommended values:
	//   "trigger"        — starts a graph (cron, webhook)
	//   "flow_control"   — branch, merge, split, sleep
	//   "transformation" — pure data manipulation
	//   "io"             — filesystem operations
	//   "network"        — HTTP and other network protocols
	//   "ai"             — LLM calls (claude, openai, ...)
	//   "external"       — MCP tools and remote gRPC modules
	//   "system"         — internal / admin
	// Empty is allowed but discouraged — the search API can't bucket
	// a module without a category.
	Category string `json:"category,omitempty"`

	// Provider names the org/vendor behind the module. Examples:
	//   "internal"          — built-in
	//   "anthropic"         — Anthropic's LLMs
	//   "mcp:<server-name>" — MCP-hosted, attributed to the server
	//   "remote:<host>"     — customer-registered remote module
	// This is metadata only — the daemon doesn't verify the claim.
	// Real provider trust requires module signing (out of scope).
	Provider string `json:"provider,omitempty"`

	// Integration is the catalog grouping label — the vendor/service the
	// node lives under in the palette UI (e.g. "Git", "ntfy",
	// "Anthropic", "Email"). All modules with the same Integration string
	// surface together under one heading. Leave empty for standard-
	// library modules (branch, merge, file_read, ...) that don't belong
	// to a specific vendor; those fall back to Category-based grouping.
	Integration string `json:"integration,omitempty"`

	// Tags are free-form keywords for finer-grained discovery. Search
	// filters match any tag (OR semantics within the tags slice).
	Tags []string `json:"tags,omitempty"`

	// Description is a longer human-readable description than Label.
	// Used for tooltips and as a search target. Label is for chips/
	// titles (short); Description can be 1–2 sentences.
	Description string `json:"description,omitempty"`

	// Icon is a logical icon name the UI maps to a glyph in its icon
	// set (today: lucide-react). Values are kebab-case lowercase, e.g.
	// "webhook", "git-branch", "sparkles". When empty the UI falls
	// back to a category-derived default. Keep this stable across
	// versions — the UI relies on it for in-canvas node identity.
	Icon string `json:"icon,omitempty"`

	// BrandLogo is an asset path (or URL) for a vendor mark — the
	// recognizable logo for a third-party service (Excel's green X,
	// GitHub's octocat, Slack's hash). When set, the UI prefers it
	// over Icon in the catalog/palette and falls back to Icon inside
	// the graph canvas (where a small glyph reads better than a
	// detailed logo). Paths starting with "/" are resolved against
	// the web app's static asset root (web/public); full URLs are
	// also accepted but discouraged (offline reliability, brand
	// guideline drift). Leave empty for first-party modules — Icon
	// alone is the right choice for built-ins like branch/merge/file_*.
	BrandLogo string `json:"brand_logo,omitempty"`

	// AwaitsApproval signals that this module pauses for external
	// resume. When true, the engine populates Job.ApprovalURL before
	// Execute and the worker treats a Result with Status="awaiting"
	// as a pause (not a terminal write). Only await_approval sets it
	// today.
	AwaitsApproval bool `json:"awaits_approval,omitempty"`

	// SubmitsChildGraph signals that this module returns awaiting plus
	// child-graph metadata in its Result, and the worker should hand
	// the result off to the SubGraphRunner to actually submit the
	// child. The dispatcher will resume the parent when the child
	// terminates. Only subgraph sets it today.
	SubmitsChildGraph bool `json:"submits_child_graph,omitempty"`
}

func (m Manifest) Input(name string) (Port, bool) {
	for _, p := range m.Inputs {
		if p.Port == name {
			return p, true
		}
	}
	return Port{}, false
}

func (m Manifest) Output(name string) (Port, bool) {
	for _, p := range m.Outputs {
		if p.Port == name {
			return p, true
		}
	}
	return Port{}, false
}
