package core

import (
	"encoding/json"
	"strings"
)

// ConnectionSlug normalises an Integration label into the slug used in
// connection storage keys (lowercase, spaces → hyphens). Mirrors the web
// integrationSlug so both sides agree on the key for a given integration.
func ConnectionSlug(integration string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(integration)), " ", "-")
}

// ConnectionSecretKey is the tenant-secret name a connection field is
// stored under: conn.<slug>.<field>. Dots (not slashes) because the
// secret-name validator only allows [A-Za-z0-9_.-] — same convention as
// the oauth.<provider>.<account> keys. The web hides the conn. prefix
// from the raw Secrets list; these are managed via the integration's
// connection card, not as ad-hoc secrets.
func ConnectionSecretKey(integration, fieldKey string) string {
	return "conn." + ConnectionSlug(integration) + "." + fieldKey
}

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

// ConnectionRequirement is one credential a drop needs. Kind is
// either "oauth" (the user authorizes via a provider; check via
// list_connections; mint a URL via start_connection) or "secret" (the
// user pastes an API key the LLM stores via set_secret; flows
// reference it as ${tenant:NAME}). Name carries the provider ID for
// oauth or the recommended secret-name slug for secret — note that
// any secret-name a node param references is acceptable; the Name
// field is just the canonical / suggested one. Note is a short human
// label the LLM can quote when asking the user.
type ConnectionRequirement struct {
	Kind string `json:"kind"`           // "oauth" | "secret"
	Name string `json:"name"`           // provider ID OR recommended secret name
	Note string `json:"note,omitempty"` // human-readable, e.g. "Anthropic API key"
}

// ConnectionField is one input of a multi-field service connection — a
// drop whose endpoint + credentials are configured once per tenant
// rather than typed on every node (ntfy's server+token, SMTP's
// host/port/user/pass). Key is the param name the drop reads; Secret
// masks the value in the UI and routes it through the engine's secret
// redaction. Fields are persisted per-tenant (under the tenant secret
// store) and, at run time, injected into any node param the author left
// unset — so a flow author only fills the per-use params (topic,
// recipient) while the connection details come from one place.
//
// A drop uses ConnectionFields OR RequiresConnections, whichever fits
// its auth shape: RequiresConnections for a single secret / OAuth
// account, ConnectionFields for an endpoint-plus-credential bundle.
type ConnectionField struct {
	Key         string `json:"key"`                   // param name the drop reads (e.g. "server", "token")
	Label       string `json:"label"`                 // human field label
	Secret      bool   `json:"secret,omitempty"`      // mask + redact (token/password); false = plain (URL/host)
	Required    bool   `json:"required,omitempty"`    // counts toward "fully connected"
	Placeholder string `json:"placeholder,omitempty"` // example value shown in the field
}

// ParamsExample is one worked params example for a drop. Title is the
// short headline ("Post to #general"), Params is the literal JSON
// blob a node would have for that case, and Notes is optional prose
// for non-obvious choices. The catalog API returns these verbatim so
// an LLM can copy the shape and adjust.
type ParamsExample struct {
	Title  string          `json:"title"`
	Params json.RawMessage `json:"params"`
	Notes  string          `json:"notes,omitempty"`
}

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
	//   "logic"          — pure predicates/operators (==, >, <, …)
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

	// Summary is a ~140-character LLM-friendly one-liner: "what does
	// this drop do, in one sentence." Distinct from Description
	// (paragraph) and Label (chip text). The catalog API surfaces
	// this verbatim — keep it concrete and verb-led. Required at
	// registration; the registry rejects manifests without one.
	Summary string `json:"summary,omitempty"`

	// Examples is at least one worked params example. An LLM
	// composing a flow reads these to learn the shape; new
	// integrations must ship with at least one. Authors write them;
	// the API serves them verbatim. Required at registration.
	Examples []ParamsExample `json:"examples,omitempty"`

	// RequiresConnections lists the credentials this drop needs
	// configured before it will run. Each entry is typed (`oauth` vs
	// `secret`) so an LLM composing a flow knows whether to send the
	// user through an OAuth dance (start_connection) or ask them to
	// paste an API key (set_secret) — without trying both. Empty for
	// drops with no external auth (file IO, transforms, flow-control).
	RequiresConnections []ConnectionRequirement `json:"requires_connections,omitempty"`

	// ConnectionFields declares a multi-field service connection — the
	// endpoint + credentials a tenant configures once (on the integration
	// page) rather than on every node. At run time the engine injects any
	// configured field into a node param the author left unset, so flows
	// carry only per-use params. See ConnectionField.
	ConnectionFields []ConnectionField `json:"connection_fields,omitempty"`

	// Egress is the allowlist of external hosts a sandboxed (out-of-process)
	// drop may reach via the broker's guarded fetch — the drop's *declared*
	// network surface, enforced on top of the global SSRF guard + egress policy.
	// Each entry is a hostname; "*.example.com" matches any subdomain. Empty
	// means the drop declares no egress, so a sandboxed drop gets none (fetch is
	// refused). Ignored for in-process (trusted) drops, which fall under the
	// process-wide egress policy instead. This is what bounds exfiltration to a
	// community drop's stated destinations.
	Egress []string `json:"egress,omitempty"`

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

// PassPort is the reserved id of the universal value-passthrough pin every
// processing drop carries (modelled on Unreal's exec pin, but it threads a
// VALUE rather than execution). A value wired into the pass input is
// re-emitted unchanged on the pass output when the node succeeds — so you can
// carry a correlation id / context / payload along a chain without re-wiring
// it through every node's data ports. The frontend draws it with a distinct
// triangle symbol as the first input/output.
const PassPort = "pass"

// WithPassthrough returns m with the universal passthrough port prepended to
// its inputs and outputs. It's a no-op for drops with no inputs (sources /
// triggers — nothing upstream to thread from) and idempotent if the port is
// already present. The port is untyped (wildcard MIME, connects to anything)
// and never required.
func WithPassthrough(m Manifest) Manifest {
	if len(m.Inputs) == 0 {
		return m // sources/triggers originate flows; no pin to thread into
	}
	if _, ok := m.Input(PassPort); ok {
		return m
	}
	pin := Port{Port: PassPort, Label: "Pass-through"}
	m.Inputs = append([]Port{pin}, m.Inputs...)
	m.Outputs = append([]Port{pin}, m.Outputs...)
	return m
}

// ApplyPassthrough copies the pass input ref onto the pass output of a
// successful result, so the threaded value flows to the next node. Engines
// call this for every node, giving the passthrough pin its behaviour without
// any per-drop code. No-op when the node didn't succeed, had no pass input, or
// already emitted a pass output of its own.
func ApplyPassthrough(input map[string]Ref, result *Result) {
	if result == nil || result.Status != StatusOK {
		return
	}
	ref, ok := input[PassPort]
	if !ok {
		return
	}
	if result.Output == nil {
		result.Output = map[string]Ref{}
	}
	if _, exists := result.Output[PassPort]; !exists {
		result.Output[PassPort] = ref
	}
}
