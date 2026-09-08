// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Renders the Step catalog reference from the drop manifests, so the docs cannot
// drift from what the daemon actually serves.
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

	"github.com/dazyflow/dazyflow/core"
	_ "github.com/dazyflow/dazyflow/drops" // side-effect: register every built-in drop
	"github.com/dazyflow/dazyflow/engine"
)

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

	for _, name := range names {
		drops := groups[name]
		sort.Slice(drops, func(i, j int) bool { return dropTitle(drops[i]) < dropTitle(drops[j]) })
		page := renderGroup(name, drops)
		file := filepath.Join(outDir, slug(name)+".md")
		if err := os.WriteFile(file, []byte(page), 0o644); err != nil {
			return err
		}
	}

	if err := os.WriteFile(filepath.Join(outDir, "index.md"), []byte(renderIndex(names, groups)), 0o644); err != nil {
		return err
	}

	fmt.Printf("docsgen: wrote %d group pages + index for %d steps to %s\n",
		len(names), len(manifests), outDir)
	return nil
}

func renderIndex(names []string, groups map[string][]core.Manifest) string {
	var b strings.Builder
	frontMatter(&b, "Step catalog", "")
	b.WriteString("# Step catalog\n\n")
	b.WriteString(banner())
	b.WriteString("Every step you can add to a flow. **Apps & services** connect an outside account; " +
		"**Triggers** decide when a flow starts; **Building blocks** are the standard toolkit for moving and shaping data.\n\n")

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
	// An explicit anchor, or the auto-slug collides with a heading below.
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
	fmt.Fprintf(b, "## %s {#%s}\n\n", mdSafe(dropTitle(m)), m.ID)

	if conn := connectionNote(m); conn != "" {
		fmt.Fprintf(b, "**Connect first:** %s\n\n", conn)
	}

	// Description first, Summary as the fallback.
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

func banner() string {
	return fmt.Sprintf("> 🧭 **New to Dazyflow?** Start with [Concepts](%s) and the [Glossary](%s) — "+
		"they explain flows, steps, wiring, triggers, and the words used below.\n\n", *conceptsURL, *glossaryURL)
}

func tableLegend() string {
	return "**Reading the tables below:** an **Input** is a value you connect from an earlier step; " +
		"a **Setting** is a value you fill in on the step itself. Value types: " +
		"*text*, *item* (structured info — a row or object), *yes / no*, *file*, *anything*, " +
		"and the plural forms — *items (a table)*, *texts*, *files* — for many at once.\n\n"
}

func groupName(m core.Manifest) string {
	if strings.TrimSpace(m.Integration) != "" {
		return m.Integration
	}
	if strings.TrimSpace(m.BrandLogo) != "" {
		return m.Label
	}
	if n, ok := categoryNames[m.Category]; ok {
		return n
	}
	if m.Category != "" {
		return titleWords(strings.ReplaceAll(m.Category, "_", " "))
	}
	return "Other"
}

func titleWords(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

func bucketOf(m core.Manifest) string {
	if strings.TrimSpace(m.Integration) != "" {
		return "apps"
	}
	if m.Category == "trigger" {
		return "triggers"
	}
	return "blocks"
}

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

func dropTitle(m core.Manifest) string {
	if strings.TrimSpace(m.Subtitle) != "" {
		return m.Label + " — " + m.Subtitle
	}
	return m.Label
}

// Plain words: a MIME type means nothing to the reader of this page.
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

type paramProp struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Type        string          `json:"type"`
	Default     json.RawMessage `json:"default"`
	Advanced    bool            `json:"x_advanced"`
	Enum        []any           `json:"enum"`
	EnumNames   []string        `json:"enumNames"`
}

// A choice setting with no help text still needs a cell.
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

// Hides the x_* extensions, which are editor machinery rather than settings.
func settingRows(props map[string]paramProp, required map[string]bool) []settingRow {
	var rows []settingRow
	for name, p := range props {
		if name == "base_url" {
			continue // internal testing override, not user-facing
		}
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
		bi, bj := band(rows[i].required, rows[i].advanced), band(rows[j].required, rows[j].advanced)
		if bi != bj {
			return bi < bj
		}
		return rows[i].display < rows[j].display
	})
	return rows
}

var forcedAdvanced = map[string]bool{
	"timeout_ms": true,
	"account":    true,
	"token":      true,
}

var specialNames = map[string]string{
	"timeout_ms": "Timeout",
	"api_key":    "API key",
	"api_token":  "API token",
	"dsn":        "Connection string",
	"tz":         "Time zone",
	"url":        "URL",
	"sql":        "SQL",
}

func displayName(key, title string) string {
	if t := strings.TrimSpace(title); t != "" {
		return t
	}
	if s, ok := specialNames[key]; ok {
		return s
	}
	return prettify(key)
}

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

func frontMatter(b *strings.Builder, title, icon string) {
	fmt.Fprintf(b, "---\ntitle: %s\n", title)
	if icon != "" {
		fmt.Fprintf(b, "icon: %s\n", icon)
	}
	b.WriteString("generated: true\n---\n")
	b.WriteString("<!-- Generated by cmd/docsgen from step manifests. Do not edit by hand. -->\n\n")
}

// The first BrandLogo in the group; an integration has no record of its own.
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

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// A literal "<" in prose would otherwise be read as HTML by the Markdown
// renderer; inside a code span it must be left alone.
func mdSafe(s string) string {
	var b strings.Builder
	inCode := false
	for _, r := range s {
		switch {
		case r == '`':
			inCode = !inCode
			b.WriteRune(r)
		case inCode:
			b.WriteRune(r)
		case r == '<':
			b.WriteString("&lt;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func emptyParams(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "{}"
}

func escapeCell(s string) string {
	s = mdSafe(oneLine(s))
	return strings.ReplaceAll(s, "|", "\\|")
}

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
