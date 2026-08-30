// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command docsgen renders the user-facing "Step catalog" reference (§10 of the
// docs IA) straight from the live drop registry. Because it reads the same
// engine.Default.Manifests() the daemon serves and the contract tests assert on,
// the reference can never drift from the code: add or change a drop and its page
// regenerates from the manifest's own Summary / Description / ports / params /
// Examples.
//
// It is deliberately generator-agnostic Markdown (minimal YAML front matter +
// an HTML "generated" marker), so it drops into Docusaurus / Hugo / Astro /
// MkDocs alike. The index groups steps into three reader-facing buckets (apps,
// triggers, building blocks); each group is one page. Every page links back to
// the hand-written Concepts + Glossary so the jargon has somewhere to resolve.
//
// Usage:
//
//	go run ./cmd/docsgen -out docs/reference/steps
//
// Output is deterministic (everything sorted) so re-runs produce clean diffs.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"git.sr.ht/~klahr/dazyflow/core"
	_ "git.sr.ht/~klahr/dazyflow/drops" // side-effect: register every built-in drop
	"git.sr.ht/~klahr/dazyflow/engine"
)

// Links to the hand-written guide pages the reference leans on. Absolute
// site-root paths so they resolve regardless of how deep the reference pages
// are nested; override if the guide lives elsewhere.
var (
	conceptsURL = flag.String("concepts-url", "/guide/concepts", "site path of the Concepts page")
	glossaryURL = flag.String("glossary-url", "/guide/glossary", "site path of the Glossary page")
)

func main() {
	out := flag.String("out", "docs/reference/steps", "directory to write the reference Markdown into")
	flag.Parse()

	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "docsgen:", err)
		os.Exit(1)
	}
}

func run(outDir string) error {
	manifests := engine.Default.Manifests()
	if len(manifests) == 0 {
		return fmt.Errorf("no drops registered — is the umbrella import present?")
	}

	// Group by Integration (a vendor/app) when set, else by the friendly name of
	// the drop's Category (the standard library: triggers, flow control, …).
	groups := map[string][]core.Manifest{}
	for _, m := range manifests {
		groups[groupName(m)] = append(groups[groupName(m)], m)
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)

	// One page per group.
	for _, name := range names {
		drops := groups[name]
		sort.Slice(drops, func(i, j int) bool { return dropTitle(drops[i]) < dropTitle(drops[j]) })
		page := renderGroup(name, drops)
		file := filepath.Join(outDir, slug(name)+".md")
		if err := os.WriteFile(file, []byte(page), 0o644); err != nil {
			return err
		}
	}

	// Index.
	if err := os.WriteFile(filepath.Join(outDir, "index.md"), []byte(renderIndex(names, groups)), 0o644); err != nil {
		return err
	}

	fmt.Printf("docsgen: wrote %d group pages + index for %d steps to %s\n",
		len(names), len(manifests), outDir)
	return nil
}

// --- rendering ---------------------------------------------------------------

func renderIndex(names []string, groups map[string][]core.Manifest) string {
	var b strings.Builder
	frontMatter(&b, "Step catalog", "")
	b.WriteString("# Step catalog\n\n")
	b.WriteString(banner())
	b.WriteString("Every step you can add to a flow. **Apps & services** connect an outside account; " +
		"**Triggers** decide when a flow starts; **Building blocks** are the standard toolkit for moving and shaping data.\n\n")

	// Bucket the groups into the three reader-facing sections.
	byBucket := map[string][]string{}
	for _, name := range names {
		byBucket[bucketOf(groups[name][0])] = append(byBucket[bucketOf(groups[name][0])], name)
	}
	sections := []struct{ key, title, intro string }{
		{"apps", "Apps & services", "Connect the account once on the Apps page, then use these steps in any flow."},
		{"triggers", "Triggers", "Every flow starts with one of these — on a schedule, or when something arrives."},
		{"blocks", "Building blocks", "The standard toolkit: decisions, loops, and tools to shape text, data, files and dates."},
	}
	for _, sec := range sections {
		gs := byBucket[sec.key]
		if len(gs) == 0 {
			continue
		}
		sort.Strings(gs)
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", sec.title, sec.intro)
		for _, name := range gs {
			drops := append([]core.Manifest(nil), groups[name]...)
			sort.Slice(drops, func(i, j int) bool { return dropTitle(drops[i]) < dropTitle(drops[j]) })
			fmt.Fprintf(&b, "### [%s](./%s.md)\n\n", name, slug(name))
			for _, m := range drops {
				fmt.Fprintf(&b, "- **[%s](./%s.md#%s)** — %s\n", mdSafe(dropTitle(m)), slug(name), m.ID, mdSafe(oneLine(m.Summary)))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func renderGroup(name string, drops []core.Manifest) string {
	var b strings.Builder
	frontMatter(&b, name, groupBrand(drops))
	// Give the page title an explicit anchor so its auto-slug can't collide with
	// a drop section's anchor — e.g. the "ChatGPT" group heading would otherwise
	// slug to "chatgpt", clashing with the ChatGPT — Ask drop's {#chatgpt}. The
	// leading underscore guarantees it never equals a drop id.
	fmt.Fprintf(&b, "# %s {#_group}\n\n", name)
	b.WriteString(banner())
	b.WriteString(tableLegend())
	for i, m := range drops {
		if i > 0 {
			b.WriteString("\n---\n\n")
		}
		renderDrop(&b, m)
	}
	return b.String()
}

func renderDrop(b *strings.Builder, m core.Manifest) {
	// A stable anchor on the drop ID so the index can deep-link to it.
	fmt.Fprintf(b, "## %s {#%s}\n\n", mdSafe(dropTitle(m)), m.ID)

	// Connection setup, in plain terms.
	if conn := connectionNote(m); conn != "" {
		fmt.Fprintf(b, "**Connect first:** %s\n\n", conn)
	}

	// Lead paragraph: prefer the fuller Description and fall back to the Summary,
	// so we never print both — they overlap and read as repetition. (The Summary
	// still powers the one-liner in the index.)
	lead := strings.TrimSpace(m.Description)
	if lead == "" {
		lead = oneLine(m.Summary)
	}
	if lead != "" {
		fmt.Fprintf(b, "%s\n\n", mdSafe(lead))
	}

	renderPorts(b, "Inputs (connect these from earlier steps)", m.Inputs, true)
	renderSettings(b, m)
	renderPorts(b, "Outputs (what comes out)", m.Outputs, false)

	if beh := behavior(m); beh != "" {
		fmt.Fprintf(b, "**Behaviour:** %s\n\n", beh)
	}

	renderExamples(b, m)

	if len(m.Tags) > 0 {
		tags := make([]string, len(m.Tags))
		for i, t := range m.Tags {
			tags[i] = "`" + t + "`"
		}
		fmt.Fprintf(b, "*Keywords: %s*\n\n", strings.Join(tags, " "))
	}
}

func renderPorts(b *strings.Builder, heading string, ports []core.Port, showRequired bool) {
	if len(ports) == 0 {
		return
	}
	fmt.Fprintf(b, "**%s**\n\n", heading)
	if showRequired {
		b.WriteString("| Name | Type | Required |\n| --- | --- | --- |\n")
		for _, p := range ports {
			fmt.Fprintf(b, "| %s | %s | %s |\n", portLabel(p), humanKind(p), yesNo(p.Required))
		}
	} else {
		b.WriteString("| Name | Type |\n| --- | --- |\n")
		for _, p := range ports {
			fmt.Fprintf(b, "| %s | %s |\n", portLabel(p), humanKind(p))
		}
	}
	b.WriteString("\n")
}

func renderSettings(b *strings.Builder, m core.Manifest) {
	props, required := parseParams(m.ParamsSchema)
	rows := settingRows(props, required)
	if len(rows) == 0 {
		return
	}
	b.WriteString("**Settings (fill these in on the step)**\n\n")
	b.WriteString("| Setting | What it does | Required | Default |\n| --- | --- | --- | --- |\n")
	for _, r := range rows {
		desc := escapeCell(r.desc)
		if desc == "" {
			desc = "—"
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s |\n", r.display, desc, yesNo(r.required), r.def)
	}
	b.WriteString("\n")
}

func renderExamples(b *strings.Builder, m core.Manifest) {
	for _, ex := range m.Examples {
		if ex.Title != "" {
			fmt.Fprintf(b, "**Example — %s**\n\n", ex.Title)
		}
		if emptyParams(ex.Params) {
			// An empty {} means "nothing to fill in" — a bare code block here just
			// confuses. Say so in words; the note below explains the wiring.
			b.WriteString("*No settings to fill in for this example — it's about how the step is connected (see below).*\n\n")
		} else {
			var pretty bytes.Buffer
			if json.Indent(&pretty, ex.Params, "", "  ") == nil {
				b.WriteString("Settings for this example:\n\n")
				fmt.Fprintf(b, "```json\n%s\n```\n\n", pretty.String())
			}
		}
		if ex.Notes != "" {
			fmt.Fprintf(b, "%s\n\n", mdSafe(ex.Notes))
		}
	}
}

// --- helpers -----------------------------------------------------------------

// banner is the "new here?" pointer at the top of every page.
func banner() string {
	return fmt.Sprintf("> 🧭 **New to Dazyflow?** Start with [Concepts](%s) and the [Glossary](%s) — "+
		"they explain flows, steps, wiring, triggers, and the words used below.\n\n", *conceptsURL, *glossaryURL)
}

// tableLegend explains, once per group page, the Input/Setting split and the
// value-type words the tables use — the two things a first-time reader trips on.
func tableLegend() string {
	return "**Reading the tables below:** an **Input** is a value you connect from an earlier step; " +
		"a **Setting** is a value you fill in on the step itself. Value types: " +
		"*text*, *item* (structured info — a row or object), *yes / no*, *file*, *anything*, " +
		"and the plural forms — *items (a table)*, *texts*, *files* — for many at once.\n\n"
}

// groupName is the catalog heading a drop lives under: its Integration (an app
// like "Klarna"), else the friendly name of its Category (the standard library).
func groupName(m core.Manifest) string {
	if strings.TrimSpace(m.Integration) != "" {
		return m.Integration
	}
	// A branded drop with no Integration (e.g. RSS) is a connectionless source
	// with its own identity — give it a dedicated group (by Label), with its
	// brand mark, instead of lumping it into a category catch-all like Network &
	// HTTP. bucketOf is unchanged, so it still reads as a building block.
	if strings.TrimSpace(m.BrandLogo) != "" {
		return m.Label
	}
	if n, ok := categoryNames[m.Category]; ok {
		return n
	}
	if m.Category != "" {
		return strings.Title(strings.ReplaceAll(m.Category, "_", " ")) //nolint:staticcheck // ASCII slugs
	}
	return "Other"
}

// bucketOf sorts a drop into one of the index's three reader-facing sections.
// Integration wins (an app-specific trigger belongs with its app), then a bare
// trigger category, else the standard-library "building blocks".
func bucketOf(m core.Manifest) string {
	if strings.TrimSpace(m.Integration) != "" {
		return "apps"
	}
	if m.Category == "trigger" {
		return "triggers"
	}
	return "blocks"
}

// categoryNames maps the manifest Category slugs onto reader-friendly headings.
var categoryNames = map[string]string{
	"trigger":        "Triggers",
	"flow_control":   "Flow control",
	"logic":          "Logic & comparisons",
	"transformation": "Transform data",
	"io":             "Files",
	"network":        "Network & HTTP",
	"ai":             "AI",
	"external":       "External tools",
	"system":         "System",
}

// dropTitle is the drop's display title: "Label — Subtitle" when a subtitle
// disambiguates several actions under one app, else just the Label.
func dropTitle(m core.Manifest) string {
	if strings.TrimSpace(m.Subtitle) != "" {
		return m.Label + " — " + m.Subtitle
	}
	return m.Label
}

// humanKind renders a port's value kind in plain words (never a MIME type).
//
// These are deliberately the SAME words the canvas puts on a pin — see
// portTypeLabel in web/src/lib/ports.ts. The docs used to teach their own
// vocabulary ("data", "list of data") while the product said "Item" and "Items
// (a table)", so a reader who had just finished the concepts page had to
// translate every type on sight. Keep the two in step: change one, change both.
//
// "Item" is the umbrella for structured JSON (a row, an object, a number, a
// count) — deliberately vaguer than "record" so a scalar isn't mislabelled.
func humanKind(p core.Port) string {
	many := p.Cardinality() == core.Many
	switch p.Kind() {
	case core.KindText:
		if many {
			return "texts"
		}
		return "text"
	case core.KindBool:
		return "yes / no"
	case core.KindFile:
		if many {
			return "files"
		}
		return "file"
	case core.KindItem, core.KindNumber:
		if many {
			return "items (a table)"
		}
		return "item"
	}
	return "anything"
}

func portLabel(p core.Port) string {
	if strings.TrimSpace(p.Label) != "" {
		return mdSafe(p.Label)
	}
	return mdSafe(p.Port)
}

// connectionNote describes, in one plain sentence, what the user must connect
// before this step will run — derived from ConnectionFields / RequiresConnections.
func connectionNote(m core.Manifest) string {
	if len(m.ConnectionFields) > 0 {
		app := m.Integration
		if app == "" {
			app = m.Label
		}
		return fmt.Sprintf("Connect your %s account once on the Apps page.", app)
	}
	if len(m.RequiresConnections) > 0 {
		var notes []string
		for _, c := range m.RequiresConnections {
			switch {
			case c.Note != "":
				notes = append(notes, c.Note)
			case c.Kind == "oauth":
				notes = append(notes, "sign in to "+c.Name)
			case c.Name != "":
				notes = append(notes, "an API key ("+c.Name+")")
			}
		}
		if len(notes) > 0 {
			return "Needs " + strings.Join(notes, ", ") + ", set up on the Apps page."
		}
	}
	return ""
}

// behavior turns the retry/idempotency flags into a reassurance a non-technical
// reader can act on ("safe to retry" vs "runs once").
func behavior(m core.Manifest) string {
	switch {
	case m.RetryPolicy == core.RetryNever && m.DedupeWrites:
		return "Runs once. It does something that shouldn't happen twice (like sending a message or moving money), so Dazyflow won't automatically try it again."
	case m.RetryPolicy == core.RetryNever:
		return "Not retried automatically if it fails."
	case m.RetryPolicy == core.RetryExponentialBackoff:
		return "Safe to retry — if it fails, Dazyflow tries again automatically with a growing delay."
	}
	return ""
}

// --- params schema parsing ---------------------------------------------------

type paramProp struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Type        string          `json:"type"`
	Default     json.RawMessage `json:"default"`
	Advanced    bool            `json:"x_advanced"`
	Enum        []any           `json:"enum"`
	EnumNames   []string        `json:"enumNames"`
}

// enumHint synthesises a "What it does" cell for a choice setting that has no
// description of its own — the docs otherwise hide the options the app shows as
// a dropdown. Prefers the friendly enumNames, falls back to the raw enum values.
func enumHint(p paramProp) string {
	names := p.EnumNames
	if len(names) == 0 {
		for _, e := range p.Enum {
			if s, ok := e.(string); ok {
				names = append(names, s)
			}
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "Choose one: " + strings.Join(names, ", ") + "."
}

type paramsSchema struct {
	Properties map[string]paramProp `json:"properties"`
	Required   []string             `json:"required"`
}

func parseParams(raw json.RawMessage) (map[string]paramProp, map[string]bool) {
	var s paramsSchema
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return nil, nil
	}
	req := map[string]bool{}
	for _, r := range s.Required {
		req[r] = true
	}
	return s.Properties, req
}

type settingRow struct {
	display  string // reader-facing label — never the raw snake_case key
	desc     string
	required bool
	advanced bool
	def      string
}

// settingRows turns the schema properties into display rows, hiding the internal
// base_url test seam and ordering them the way a reader scans: required first,
// then optional, advanced (x_advanced) last, alphabetical within each band. The
// display name is the manifest title (or a prettified key) so the table never
// shows a bare param id — matching the friendly labels the Inputs table uses.
func settingRows(props map[string]paramProp, required map[string]bool) []settingRow {
	var rows []settingRow
	for name, p := range props {
		if name == "base_url" {
			continue // internal testing override, not user-facing
		}
		// timeout_ms / account / token are technical plumbing present on many
		// steps — sink them to the advanced band so they stop crowding the
		// settings a reader actually cares about.
		adv := p.Advanced || forcedAdvanced[name]
		desc := p.Description
		if desc == "" {
			desc = enumHint(p) // show the dropdown choices when there's no prose
		}
		if desc == "" && (name == "account" || name == "token") {
			desc = "Set by your connected account — normally leave this alone."
		}
		rows = append(rows, settingRow{
			display:  displayName(name, p.Title),
			desc:     desc,
			required: required[name],
			advanced: adv,
			def:      defaultString(p.Default, adv),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		// Band: required (0) < optional (1) < advanced (2).
		bi, bj := band(rows[i].required, rows[i].advanced), band(rows[j].required, rows[j].advanced)
		if bi != bj {
			return bi < bj
		}
		return rows[i].display < rows[j].display
	})
	return rows
}

// forcedAdvanced are param keys that are connection/plumbing knobs on many
// steps; the generator sinks them to the advanced band regardless of the
// manifest, so the settings a reader cares about stay up top.
var forcedAdvanced = map[string]bool{
	"timeout_ms": true,
	"account":    true,
	"token":      true,
}

// specialNames maps a few internal param keys that lack a manifest title (or
// whose title would prettify awkwardly) to a reader-friendly label.
var specialNames = map[string]string{
	"timeout_ms": "Timeout",
	"api_key":    "API key",
	"api_token":  "API token",
	"dsn":        "Connection string",
	"tz":         "Time zone",
	"url":        "URL",
	"sql":        "SQL",
}

// displayName is the reader-facing setting label: the manifest title when set,
// else a special-cased or prettified version of the raw key — never the bare
// snake_case id.
func displayName(key, title string) string {
	if t := strings.TrimSpace(title); t != "" {
		return t
	}
	if s, ok := specialNames[key]; ok {
		return s
	}
	return prettify(key)
}

// prettify turns a snake_case key into Sentence case: create_table → "Create table".
func prettify(key string) string {
	s := strings.ReplaceAll(key, "_", " ")
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func band(required, advanced bool) int {
	switch {
	case advanced:
		return 2
	case required:
		return 0
	default:
		return 1
	}
}

func defaultString(raw json.RawMessage, advanced bool) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		if advanced {
			return "*(advanced)*"
		}
		return "—"
	}
	s = strings.Trim(s, `"`)
	if advanced {
		return "`" + s + "` *(advanced)*"
	}
	return "`" + s + "`"
}

// --- text utilities ----------------------------------------------------------

func frontMatter(b *strings.Builder, title, icon string) {
	fmt.Fprintf(b, "---\ntitle: %s\n", title)
	if icon != "" {
		// The vendor mark (e.g. /brands/gmail.svg) so the docs sidebar + page
		// header render the app's brand icon for this group.
		fmt.Fprintf(b, "icon: %s\n", icon)
	}
	b.WriteString("generated: true\n---\n")
	b.WriteString("<!-- Generated by cmd/docsgen from step manifests. Do not edit by hand. -->\n\n")
}

// groupBrand is the vendor mark shared by a group's drops (the first BrandLogo).
// groupName splits every branded drop into a vendor group (by Integration) or a
// standalone branded group (by Label, e.g. RSS), so any group that still lands
// in a category bucket (Network & HTTP, Files, …) has only unbranded primitives
// — no BrandLogo to inherit. Hence returning the first BrandLogo can't leak a
// lone member's logo onto a category group.
func groupBrand(drops []core.Manifest) string {
	for _, m := range drops {
		if m.BrandLogo != "" {
			return m.BrandLogo
		}
	}
	return ""
}

func yesNo(v bool) string {
	if v {
		return "Yes"
	}
	return "No"
}

// oneLine collapses internal newlines so a summary sits on one Markdown line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// mdSafe makes a prose string safe for VitePress, which compiles Markdown
// through Vue: a literal "<place>" reads as an unclosed HTML tag and "{{x}}" as
// a Vue interpolation, both of which break the build. We escape "<" and "{{"
// — but only OUTSIDE inline code spans, where VitePress already treats the
// content as raw text (escaping there would show the entities literally).
func mdSafe(s string) string {
	var b strings.Builder
	inCode := false
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '`' {
			inCode = !inCode
			b.WriteRune(r)
			continue
		}
		if inCode {
			b.WriteRune(r)
			continue
		}
		switch {
		case r == '<':
			b.WriteString("&lt;")
		case r == '{' && i+1 < len(rs) && rs[i+1] == '{':
			b.WriteString("&#123;&#123;")
			i++
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// emptyParams reports whether an example's params are absent or an empty object
// — the case where a raw {} code block reads as a confusing non-example.
func emptyParams(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "{}"
}

// escapeCell keeps a description safe inside a Markdown table cell (pipes and
// newlines would break the row).
func escapeCell(s string) string {
	s = mdSafe(oneLine(s))
	return strings.ReplaceAll(s, "|", "\\|")
}

// slug turns a group name into a filesystem- and URL-safe basename.
func slug(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
