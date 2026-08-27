// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"encoding/json"
	"slices"
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
// reference it as ${secret.NAME}). Name carries the provider ID for
// oauth or the recommended secret-name slug for secret — note that
// any secret-name a node param references is acceptable; the Name
// field is just the canonical / suggested one. Note is a short human
// label the LLM can quote when asking the user.
type ConnectionRequirement struct {
	Kind string `json:"kind" xml:"kind"`                     // "oauth" | "secret"
	Name string `json:"name" xml:"name"`                     // provider ID OR recommended secret name
	Note string `json:"note,omitempty" xml:"note,omitempty"` // human-readable, e.g. "Anthropic API key"
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
//
// Placeholder vs Help — they are different jobs and connectors kept
// conflating them. A PLACEHOLDER is an example of the value ("sk-ant-…",
// "smtp.example.com"): it lives inside the input and DISAPPEARS on the first
// keystroke, so it can only ever show the shape of the answer. HELP is the
// instruction for finding that answer ("Create one in HA → Profile →
// Long-Lived Access Tokens"): it renders under the field and stays put while
// the user types, which is exactly when they need it. Guidance in a
// placeholder is guidance that vanishes at the moment of use — and gets
// truncated by the input width before that.
type ConnectionField struct {
	Key         string   `json:"key" xml:"key"`                                     // param name the drop reads (e.g. "server", "token")
	Label       string   `json:"label" xml:"label"`                                 // human field label
	Secret      bool     `json:"secret,omitempty" xml:"secret,omitempty"`           // mask + redact (token/password); false = plain (URL/host)
	Required    bool     `json:"required,omitempty" xml:"required,omitempty"`       // counts toward "fully connected"
	Placeholder string   `json:"placeholder,omitempty" xml:"placeholder,omitempty"` // EXAMPLE VALUE shown in the field ("smtp.example.com") — not instructions; see Help
	Help        string   `json:"help,omitempty" xml:"help,omitempty"`               // one line of setup guidance, rendered UNDER the input and always visible
	Options     []string `json:"options,omitempty" xml:"options>option,omitempty"`  // when set, the field is an enum — UI renders a dropdown of these values plus a blank "default"
}

// ParamsExample is one worked params example for a drop. Title is the
// short headline ("Post to #general"), Params is the literal JSON
// blob a node would have for that case, and Notes is optional prose
// for non-obvious choices. The catalog API returns these verbatim so
// an LLM can copy the shape and adjust.
type ParamsExample struct {
	Title  string          `json:"title" xml:"title"`
	Params json.RawMessage `json:"params" xml:"params"`
	Notes  string          `json:"notes,omitempty" xml:"notes,omitempty"`
}

type Port struct {
	Port     string   `json:"port" xml:"port"`
	MIME     []string `json:"mime" xml:"mime>type"`
	Label    string   `json:"label" xml:"label"`
	Required bool     `json:"required" xml:"required"`
	Variadic bool     `json:"variadic" xml:"variadic"`
	Min      *int     `json:"min,omitempty" xml:"min,omitempty"`
	Max      *int     `json:"max,omitempty" xml:"max,omitempty"`
	// List marks a port that carries a LIST of records (an array of
	// {column: value} objects) rather than a single value. Set on both
	// outputs that emit a list (a trigger's new responses, a DB query's rows)
	// and inputs that consume a list (for_each's items, an insert's rows).
	// Drives the "you wired a list into a one-at-a-time step — wrap it in a
	// For each loop" hint: a List output into a non-List input is the tell.
	List bool `json:"list,omitempty" xml:"list,omitempty"`
	// InlineOnly marks an input that takes a VALUE and cannot take a file
	// reference. Set on every input of a tenant runner's drop: a Ref's path is
	// on the daemon's own disk, which means nothing to a process on another
	// machine. The editor uses it to say so on the port rather than leaving the
	// failure to a run; the engine refuses such a job before the step runs
	// (engine.refuseInlineOnlyFileRefs), whether the drop is native or remote.
	InlineOnly bool `json:"inline_only,omitempty" xml:"inline_only,omitempty"`
}

// PortKind is the value kind a port carries in the simplified data model — the
// small set of human-meaningful currencies a flow moves between steps. It's
// DERIVED from the port's existing MIME declaration (see Port.Kind), so this is
// the compatibility bridge: manifests keep their MIME for now while tooling,
// the engine, and the UI move to reading Kind/Cardinality. Phase 1 of the data-
// layer simplification (Items × one/many) — later phases tighten the mapping.
type PortKind string

const (
	KindItem PortKind = "item" // a record: a {field: value} object (application/json)
	KindText PortKind = "text" // human/plain text (text/plain, text/html)
	KindBool PortKind = "bool" // a yes/no (application/x-bool)
	KindFile PortKind = "file" // a file/blob (pdf, spreadsheet, octet-stream, …)
	KindAny  PortKind = "any"  // untyped pass-through — matches anything
	// KindNumber is reserved: numbers ride as application/json today, so they
	// currently derive as KindItem. A dedicated number signal is a later phase.
	KindNumber PortKind = "number"
)

// Cardinality is whether a port carries ONE value or MANY (a list). In the
// simplified model a "table" is just many Items; there is no separate rows type.
type Cardinality string

const (
	One  Cardinality = "one"
	Many Cardinality = "many"
)

// Kind maps the port's declared MIME types onto the simplified value model.
// Empty MIME (a wildcard / pass-through port) is KindAny. A port that lists
// several MIMEs (a flexible sink, e.g. json+text) is classified by its richest
// structured nature (Item before Text). Unknown/binary MIMEs fall through to
// KindFile. Pure derivation — it reads existing manifests and changes none.
func (p Port) Kind() PortKind {
	if len(p.MIME) == 0 {
		return KindAny
	}
	has := func(m string) bool { return slices.Contains(p.MIME, m) }
	switch {
	case has("application/json"), has("application/x-dazyflow-list+json"):
		return KindItem
	case has("text/plain"), has("text/html"):
		return KindText
	case has("application/x-bool"):
		return KindBool
	default:
		return KindFile
	}
}

// Cardinality reports whether the port carries one value or a list, from the
// existing List flag.
func (p Port) Cardinality() Cardinality {
	if p.List {
		return Many
	}
	return One
}

type Manifest struct {
	ID      string `json:"id" xml:"id"`
	Version string `json:"version" xml:"version"`
	Label   string `json:"label" xml:"label"`
	// Subtitle is an optional short action line shown under the Label on the
	// node card / inspector / palette — e.g. Label "Google Sheets" with
	// Subtitle "Append rows". Lets several drops share a product name as their
	// title and disambiguate by action. Empty → no subtitle (title only).
	Subtitle       string          `json:"subtitle,omitempty" xml:"subtitle,omitempty"`
	Color          string          `json:"color" xml:"color"`
	ExecutionModel ExecutionModel  `json:"execution_model" xml:"execution_model"`
	ProcessModel   ProcessModel    `json:"process_model" xml:"process_model"`
	Inputs         []Port          `json:"inputs" xml:"inputs>port"`
	Outputs        []Port          `json:"outputs" xml:"outputs>port"`
	ParamsSchema   json.RawMessage `json:"params_schema" xml:"params_schema"`
	Idempotent     bool            `json:"idempotent" xml:"idempotent"`
	RetryPolicy    RetryPolicy     `json:"retry_policy" xml:"retry_policy"`

	// MaxRetries lets a module author override the worker-global retry
	// cap (total attempts) for nodes running this module. Zero means
	// "unspecified — use the worker default". A flaky network module can
	// set it high ("tolerates 10"); a costly or one-shot module can set
	// it to 1 (a single attempt, no retry). Only consulted when
	// RetryPolicy already opts the module into retries.
	MaxRetries int `json:"max_retries,omitempty" xml:"max_retries,omitempty"`

	// DedupeWrites opts a NON-idempotent external write (one whose upstream
	// API has no idempotency key — Twilio SMS, Gmail send, Discord webhook,
	// Sheets append, Home Assistant call_service) into engine-side dedupe.
	// When set, the engine records a node's successful result keyed by its
	// stable job ID; if that exact node-record runs AGAIN (a worker reclaims
	// an expired lease after the first attempt already fired the write, or a
	// crash re-runs it) the engine returns the recorded result WITHOUT calling
	// the drop again — so the SMS/email/message isn't sent twice. When a list
	// auto-fans the node (one send per item), each item is deduped
	// independently, so a crash mid-fan replays only the items not yet
	// recorded. The guarantee is at-least-once: a crash in the small window
	// between the API succeeding and the result being recorded can still
	// re-fire. Has no effect unless the engine has a WriteDedupe store wired
	// (cmd/dzd).
	//
	// LIMITATION: dedupe keys off the node's stable job-record ID, which only
	// the top-level run assigns. A drop placed inside a for_each LOOP BODY runs
	// under a per-iteration ID that isn't persisted as a reclaimable record, so
	// DedupeWrites does not protect writes there — a body re-run can re-fire
	// them. Keep non-idempotent sends at the top level (auto-fan covers the
	// common "send to each of a list" case) rather than inside a loop body.
	DedupeWrites bool `json:"dedupe_writes,omitempty" xml:"dedupe_writes,omitempty"`

	CompatibleWith []string `json:"compatible_with" xml:"compatible_with>id"`

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
	Category string `json:"category,omitempty" xml:"category,omitempty"`

	// Provider names the org/vendor behind the module. Examples:
	//   "internal"          — built-in
	//   "anthropic"         — Anthropic's LLMs
	//   "mcp:<server-name>" — MCP-hosted, attributed to the server
	//   "remote:<host>"     — customer-registered remote module
	// This is metadata only — the daemon doesn't verify the claim.
	// Real provider trust requires module signing (out of scope).
	Provider string `json:"provider,omitempty" xml:"provider,omitempty"`

	// Integration is the catalog grouping label — the vendor/service the
	// node lives under in the palette UI (e.g. "Git", "ntfy",
	// "Anthropic", "Email"). All modules with the same Integration string
	// surface together under one heading. Leave empty for standard-
	// library modules (branch, merge, file_read, ...) that don't belong
	// to a specific vendor; those fall back to Category-based grouping.
	Integration string `json:"integration,omitempty" xml:"integration,omitempty"`

	// IntegrationDescription is a blurb about the INTEGRATION, not about this
	// module — what the service is and what an org can do with it. It is what
	// the Apps page shows under the app's name, and what the catalog API reports
	// as an integration's summary.
	//
	// Carried per manifest, and borrowed for the group by whichever module
	// declares it first, for the same reason BrandLogo is: an integration is a
	// derived view over manifests and has no record of its own to hang prose on.
	// Modules in one integration are expected to agree, or to leave it empty and
	// let one of them speak.
	//
	// Built-in integrations do NOT set this — their prose is curated (see
	// daemon.integrationSummaries and web/src/integrationMeta.ts, which can be
	// translated and edited without a release of the drop). It exists for the
	// integrations an ORG creates, where nobody but that org can write the
	// sentence: today, a described web API.
	IntegrationDescription string `json:"integration_description,omitempty" xml:"integration_description,omitempty"`

	// Tags are free-form keywords for finer-grained discovery. Search
	// filters match any tag (OR semantics within the tags slice).
	Tags []string `json:"tags,omitempty" xml:"tags>tag,omitempty"`

	// SearchBoost nudges this drop's relevance for fuzzy/tag matches
	// (not exact id/label hits). Default 0. Use it to break ties between
	// drops that match the same generic verb: e.g. SQLite "Insert rows"
	// boosts itself for "save"/"database" so the canonical
	// save-to-a-database default outranks the no-setup KV store, which
	// matches the same terms. Negative values down-rank without dropping
	// the match (floored to 1).
	SearchBoost int `json:"search_boost,omitempty" xml:"search_boost,omitempty"`

	// Description is a longer human-readable description than Label.
	// Used for tooltips and as a search target. Label is for chips/
	// titles (short); Description can be 1–2 sentences.
	Description string `json:"description,omitempty" xml:"description,omitempty"`

	// Summary is a ~140-character LLM-friendly one-liner: "what does
	// this drop do, in one sentence." Distinct from Description
	// (paragraph) and Label (chip text). The catalog API surfaces
	// this verbatim — keep it concrete and verb-led. Required at
	// registration; the registry rejects manifests without one.
	Summary string `json:"summary,omitempty" xml:"summary,omitempty"`

	// Examples is at least one worked params example. An LLM
	// composing a flow reads these to learn the shape; new
	// integrations must ship with at least one. Authors write them;
	// the API serves them verbatim. Required at registration.
	Examples []ParamsExample `json:"examples,omitempty" xml:"examples>example,omitempty"`

	// RequiresConnections lists the credentials this drop needs
	// configured before it will run. Each entry is typed (`oauth` vs
	// `secret`) so an LLM composing a flow knows whether to send the
	// user through an OAuth dance (start_connection) or ask them to
	// paste an API key (set_secret) — without trying both. Empty for
	// drops with no external auth (file IO, transforms, flow-control).
	RequiresConnections []ConnectionRequirement `json:"requires_connections,omitempty" xml:"requires_connections>connection,omitempty"`

	// ConnectionFields declares a multi-field service connection — the
	// endpoint + credentials a tenant configures once (on the integration
	// page) rather than on every node. At run time the engine injects any
	// configured field into a node param the author left unset, so flows
	// carry only per-use params. See ConnectionField.
	ConnectionFields []ConnectionField `json:"connection_fields,omitempty" xml:"connection_fields>field,omitempty"`

	// ConnectionVerifiable reports whether the integration this drop belongs
	// to has a registered live connection check (engine.ConnectionVerifier).
	// It is NOT set at registration — the daemon computes it on the way out
	// (see Service.ListDrops) so the Apps page knows whether to offer a "Test
	// connection" affordance and to verify credentials before saving them.
	ConnectionVerifiable bool `json:"connection_verifiable,omitempty" xml:"connection_verifiable,omitempty"`

	// Disabled reports that a platform admin has switched this drop off (a
	// killswitch, globally or for the caller's tenant). Like
	// ConnectionVerifiable it is NOT set at registration — the daemon computes
	// it on the way out, and only for editor-facing listings that opt in
	// (DropSearch.IncludeDisabled). Other surfaces hide disabled drops
	// entirely. The editor uses it to show the drop greyed-out and un-pickable
	// instead of having it silently vanish from the palette. The engine
	// resolver still hard-blocks execution if a disabled drop is referenced.
	Disabled bool `json:"disabled,omitempty" xml:"disabled,omitempty"`

	// Unavailable reports that this drop's provider is registered but not
	// reachable right now — an MCP server whose endpoint is down or whose
	// credential has been rotated away.
	//
	// The difference from Disabled matters: a disabled drop was switched off
	// deliberately and there is nothing for the author to do, while an
	// unavailable one is a configuration the author (or their admin) can fix,
	// and it is expected to come back.
	//
	// A drop that is unavailable is still DESCRIBED in full — its ports, its
	// params schema, its icon — because a flow already referencing it has to
	// keep its wiring. Losing the description is what turns a disconnected
	// server into a flow that appears to have lost its edges. The engine still
	// refuses to execute it; see the MCP transport.
	Unavailable bool `json:"unavailable,omitempty" xml:"unavailable,omitempty"`

	// Egress is the allowlist of external hosts a sandboxed (out-of-process)
	// drop may reach via the broker's guarded fetch — the drop's *declared*
	// network surface, enforced on top of the global SSRF guard + egress policy.
	// Each entry is a hostname; "*.example.com" matches any subdomain. Empty
	// means the drop declares no egress, so a sandboxed drop gets none (fetch is
	// refused). Ignored for in-process (trusted) drops, which fall under the
	// process-wide egress policy instead. This is what bounds exfiltration to a
	// community drop's stated destinations.
	Egress []string `json:"egress,omitempty" xml:"egress>host,omitempty"`

	// Icon is a logical icon name the UI maps to a glyph in its icon
	// set (today: lucide-react). Values are kebab-case lowercase, e.g.
	// "webhook", "git-branch", "sparkles". When empty the UI falls
	// back to a category-derived default. Keep this stable across
	// versions — the UI relies on it for in-canvas node identity.
	Icon string `json:"icon,omitempty" xml:"icon,omitempty"`

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
	BrandLogo string `json:"brand_logo,omitempty" xml:"brand_logo,omitempty"`

	// AwaitsApproval signals that this module pauses for external
	// resume. When true, the engine populates Job.ApprovalURL before
	// Execute and the worker treats a Result with Status="awaiting"
	// as a pause (not a terminal write). Only await_approval sets it
	// today.
	AwaitsApproval bool `json:"awaits_approval,omitempty" xml:"awaits_approval,omitempty"`

	// SubmitsChildGraph signals that this module returns awaiting plus
	// child-graph metadata in its Result, and the worker should hand
	// the result off to the SubGraphRunner to actually submit the
	// child. The dispatcher will resume the parent when the child
	// terminates. Only subgraph sets it today.
	SubmitsChildGraph bool `json:"submits_child_graph,omitempty" xml:"submits_child_graph,omitempty"`

	// NoPassthrough opts a drop OUT of the universal value-passthrough pin
	// (WithPassthrough). The pin makes sense on linear processing drops that
	// carry a single payload, but is wrong on two roles that set this:
	//   - pure predicates (comparators / Compare): they emit a 1/0 verdict,
	//     not a payload to thread; the pin is noise and, on the compact
	//     operator chip, it would even steal an operand slot.
	//   - pure routers (Branch): pass is emitted on every success regardless
	//     of which port the payload took, so a node wired to it fires on
	//     BOTH branches — punching a hole straight through the routing.
	NoPassthrough bool `json:"no_passthrough,omitempty" xml:"no_passthrough,omitempty"`

	// ValueSource marks a drop as a literal value source (Text, Number): an
	// input-less node whose output is authored in a required param, not wired
	// or fetched. It's the one kind of zero-input drop that genuinely
	// ORIGINATES data, so WithPassthrough gives it no pass pin (you can't wire
	// into a literal) — and the canvas renders its param as an editable field
	// that IS the node. Every OTHER input-less drop is treated as an action
	// (a fetcher / API call that configures from params but sits mid-flow) and
	// gets the pass pin by default, so authors don't set this. Distinct from a
	// trigger, which originates a flow from an external event rather than a
	// literal — triggers opt out via Category/ExecutionTrigger instead.
	ValueSource bool `json:"value_source,omitempty" xml:"value_source,omitempty"`

	// NodeState, when set, declares that a node of this drop keeps per-node
	// state across runs (a dedupe cursor, a poll watermark, an HTTP cache).
	// Its presence is the signal the editor keys off to show a "Reset state"
	// action and a "keeps state" indicator on the node — drops without it are
	// stateless and get neither, so the affordance stays off the majority. The
	// daemon clears the underlying reserved store keys via the state-reset
	// registry (engine.RegisterStateReset); this field is only the user-facing
	// description of what that state is and what resetting does.
	NodeState *NodeState `json:"node_state,omitempty" xml:"node_state,omitempty"`
}

// NodeState is the user-facing description of the persisted per-node state a
// stateful drop carries (see Manifest.NodeState). Label names the state in
// plain language ("Remembered items"); ResetHint explains what clearing it
// does, shown on the reset action's confirm.
type NodeState struct {
	Label     string `json:"label" xml:"label"`
	ResetHint string `json:"reset_hint,omitempty" xml:"reset_hint,omitempty"`
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

// MIMEBool is the port type for a boolean value (true/false). Predicates
// like In Range emit it on their result, and Branch's condition input
// expects it — a dedicated type (vs. bare application/json) so the canvas
// colours boolean pins distinctly and they read as a yes/no signal.
const MIMEBool = "application/x-bool"

// WithPassthrough returns m with the universal passthrough port prepended to
// its inputs and outputs. The port is untyped (wildcard MIME, connects to
// anything), never required, and idempotent if already present.
//
// The pin belongs on every node that sits mid-flow — including actions that
// take no DATA input and configure entirely from params (a Slack "list
// channels", an HTTP GET, a DB query): they're sequenced after an upstream
// node and want to thread a correlation value, exactly like a node with data
// inputs. So a zero-input drop gets the pin too, with two deliberate
// exceptions that genuinely ORIGINATE a flow and so have nothing upstream to
// thread from:
//
//   - triggers (Category "trigger" / ExecutionTrigger): fired by external
//     events, not wired into.
//   - value sources (ValueSource: Text, Number): a literal you author in a
//     required param, not an action you wire into. A pass INPUT would also
//     give them a declared input, flipping the canvas's value-source
//     rendering (the editable field that IS the node).
//
// Adding the pass input is itself the signal the frontend keys off: a drop is
// a value source on the canvas iff it has no declared inputs, so the two stay
// in lock-step without a separate frontend rule.
func WithPassthrough(m Manifest) Manifest {
	if m.NoPassthrough {
		return m // predicates/routers opt out — see NoPassthrough.
	}
	if _, ok := m.Input(PassPort); ok {
		return m // already present — idempotent.
	}
	if m.ExecutionModel == ExecutionTrigger || m.Category == "trigger" {
		return m // triggers originate flows; nothing upstream to thread from.
	}
	if m.ValueSource {
		return m // literal value source (Text, Number) — authored, not wired.
	}
	pin := Port{Port: PassPort, Label: "Pass-through"}
	m.Inputs = append([]Port{pin}, m.Inputs...)
	m.Outputs = append([]Port{pin}, m.Outputs...)
	return m
}

// listPortNames are the conventional port ids that carry a LIST — a set of
// records (rows/responses/…) or, for headers, a list of column names. The
// codebase names list ports consistently, so marking by name (centrally, next
// to WithPassthrough) gives every drop accurate cardinality without annotating
// each port literal. Scalar/per-item inputs (text, message, to, body, prompt,
// …) are deliberately absent, so a List output wired into one of them is the
// detectable "you fed a whole list into a one-at-a-time step" mistake.
var listPortNames = map[string]bool{
	"rows":          true,
	"headers":       true,
	"responses":     true,
	"messages":      true,
	"issues":        true,
	"events":        true,
	"subscriptions": true,
	"customers":     true,
	"items":         true,
	"results":       true,
	"records":       true,
}

// MarkListPorts returns m with List=true on every input/output port whose id is
// a conventional list-carrying name (see listPortNames). It copies the port
// slices so the registry's stored manifest is never mutated, and only ever sets
// List true — a drop that needs a different answer can set Port.List itself.
func MarkListPorts(m Manifest) Manifest {
	mark := func(ports []Port) []Port {
		if len(ports) == 0 {
			return ports
		}
		out := make([]Port, len(ports))
		copy(out, ports)
		for i := range out {
			if !out[i].List && listPortNames[out[i].Port] {
				out[i].List = true
			}
		}
		return out
	}
	m.Inputs = mark(m.Inputs)
	m.Outputs = mark(m.Outputs)
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
