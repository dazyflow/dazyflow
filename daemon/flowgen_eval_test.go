// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// The flow-generator eval: can the product's own AI build the flows people
// actually ask for, from the sentence they'd say out loud?
//
// /SCENARIOS.md is a corpus of thirty-five plain-language asks, each paired
// with a hand-built graph under tests/usecases/ that is known to compose. That
// pairing is an eval set for free: the ask is the input a real user types, the
// graph is one known-good answer, and the gate is the same one the save path
// applies. If the generator can't get there from the sentence, a non-technical
// user can't either — which is the whole promise of the feature.
//
// Two entry points:
//
//   - TestFlowGenScenariosHarness runs everywhere, with no model: it checks the
//     corpus parses, every ask has a reference graph, and the scorer agrees
//     that a reference graph answers its own ask (and that a wrong graph
//     doesn't). This keeps the harness honest as SCENARIOS.md changes.
//
//   - TestFlowGenScenarios calls the real generator and needs a key. Opt in with
//     FLOWGEN_EVAL_KEY (or ANTHROPIC_API_KEY); it is skipped otherwise, since
//     it spends money and its results are not deterministic.
//
//	FLOWGEN_EVAL_KEY=sk-ant-… go test ./daemon -run TestFlowGenScenarios -timeout 60m -v
//	FLOWGEN_EVAL_ONLY=12,29,33 FLOWGEN_EVAL_KEY=… go test ./daemon -run TestFlowGenScenarios -v
//
// Env knobs: FLOWGEN_EVAL_PROVIDER (default "claude"), FLOWGEN_EVAL_ONLY (a
// comma-separated scenario list), FLOWGEN_EVAL_OUT (report directory, default
// the test's temp dir), FLOWGEN_EVAL_MIN_VALID (fail the test below this
// percentage of valid drafts; default 0 = report only).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/llm"
)

const (
	scenariosDoc = "../SCENARIOS.md"
	referenceDir = "../tests/usecases"
)

// ask is one scenario as the buyer states it: the quoted heading and the
// acceptance sentence. Together they are what a user would type into the
// "describe your flow" box, and they are all the generator is given.
type ask struct {
	Num   int
	Title string // the quoted heading, e.g. "Chase people whose payment bounced"
	Works string // the "It works when…" sentence
}

// Prompt is the exact text handed to the generator.
func (a ask) Prompt() string {
	return a.Title + ". It works when " + strings.TrimSuffix(a.Works, ".") + "."
}

var (
	askHeading = regexp.MustCompile(`^#{2,3} (\d+)\. "(.+)"\s*$`)
	worksLine  = regexp.MustCompile(`^\*\*It works when:\*\*\s*(.*)$`)
)

// loadAsks parses the scenario corpus. The doc is the spec, so parsing it
// (rather than keeping a second copy of the asks) means the eval can't quietly
// drift away from what the document promises.
func loadAsks(t *testing.T, path string) []ask {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var asks []ask
	var cur *ask
	var collecting bool
	for _, line := range strings.Split(string(data), "\n") {
		if m := askHeading.FindStringSubmatch(line); m != nil {
			if cur != nil {
				asks = append(asks, *cur)
			}
			n, _ := strconv.Atoi(m[1])
			cur = &ask{Num: n, Title: m[2]}
			collecting = false
			continue
		}
		if cur == nil {
			continue
		}
		if m := worksLine.FindStringSubmatch(line); m != nil {
			cur.Works = strings.TrimSpace(m[1])
			collecting = true
			continue
		}
		// The acceptance sentence wraps across lines; it ends at the blank
		// line before the verdict.
		if collecting {
			if strings.TrimSpace(line) == "" {
				collecting = false
				continue
			}
			cur.Works += " " + strings.TrimSpace(line)
		}
	}
	if cur != nil {
		asks = append(asks, *cur)
	}
	return asks
}

// loadReferences groups the known-good graphs by scenario number. One ask can
// have several (an intake flow plus its sweeper, say) — the expectations are
// the union, because between them they answer the ask.
func loadReferences(t *testing.T, dir string) map[int][]core.Graph {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no reference graphs under %s: %v", dir, err)
	}
	prefix := regexp.MustCompile(`^(\d+)[a-z]?-`)
	out := map[int][]core.Graph{}
	for _, f := range files {
		m := prefix.FindStringSubmatch(filepath.Base(f))
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		data, rerr := os.ReadFile(f)
		if rerr != nil {
			t.Fatalf("read %s: %v", f, rerr)
		}
		var g core.Graph
		if jerr := json.Unmarshal(data, &g); jerr != nil {
			t.Fatalf("parse %s: %v", f, jerr)
		}
		out[n] = append(out[n], core.MigrateGraph(g))
	}
	return out
}

// glueModules are the interchangeable structural steps — shaping, routing,
// looping, formatting. Two people solving the same ask will not pick the same
// ones, and shouldn't have to, so they don't count toward whether the
// generator understood the job.
var glueModules = map[string]bool{
	"expression": true, "compute_rows": true, "map_rows": true, "render_text": true,
	"render_template": true, "render_table": true, "for_each": true, "branch": true,
	"switch": true, "compare": true, "and": true, "or": true, "not": true,
	"merge": true, "dedupe_rows": true, "sort_rows": true, "group_aggregate": true,
	"join_rows": true, "parse_json": true, "parse_csv": true, "parse_xml": true,
	"unwrap_results": true, "split_rows": true, "route_rows": true, "text": true,
	"json": true, "number": true, "date": true, "regex": true, "build_csv": true,
	"delay": true, "if": true, "eq": true, "neq": true, "gt": true, "gte": true,
	"lt": true, "lte": true, "contains": true, "phone": true, "url": true,
	"base64": true, "hash": true, "merge_rows": true, "subgraph": true,
}

// triggerKind classes how a flow starts. The generator is judged on picking
// the right KIND — a schedule, an inbound call, an app event — not on
// matching a cron expression to the minute.
func triggerKind(g core.Graph, manifests map[string]core.Manifest) string {
	for _, tr := range g.Triggers {
		switch tr.Type {
		case "cron", "poll":
			return "schedule"
		case "webhook":
			return "webhook"
		}
	}
	kind := ""
	for _, n := range g.Nodes {
		switch n.Module {
		case "cron_trigger", "poll_trigger":
			return "schedule"
		case "webhook_input":
			kind = "webhook"
		default:
			if m, ok := manifests[n.Module]; ok && m.Category == "trigger" {
				if kind == "" {
					kind = "app-event"
				}
			}
		}
	}
	if kind != "" {
		return kind
	}
	return "manual"
}

// appsUsed is the set of outside services a graph touches, by integration
// name. This is the fair granularity for "did it work out what the job
// needs": picking a different Gmail step than the reference is fine, not
// realising Gmail is involved at all is not.
func appsUsed(g core.Graph, manifests map[string]core.Manifest) map[string]bool {
	out := map[string]bool{}
	for _, n := range g.Nodes {
		m, ok := manifests[n.Module]
		if !ok || glueModules[n.Module] {
			continue
		}
		name := m.Integration
		if name == "" {
			// A built-in with no integration (Collections, the file steps,
			// the AI steps) still counts as a capability — key it by module.
			if m.Category == "trigger" || m.Category == "flow_control" || m.Category == "logic" {
				continue
			}
			name = n.Module
		}
		out[name] = true
	}
	return out
}

// score is one scenario's result.
type score struct {
	Num          int      `json:"scenario"`
	Ask          string   `json:"ask"`
	Generated    bool     `json:"generated"`
	Valid        bool     `json:"valid"`
	TriggerWant  string   `json:"trigger_want"`
	TriggerGot   string   `json:"trigger_got"`
	TriggerMatch bool     `json:"trigger_match"`
	AppsWant     []string `json:"apps_want"`
	AppsMissing  []string `json:"apps_missing"`
	AppsExtra    []string `json:"apps_extra"`
	AppCoverage  float64  `json:"app_coverage"`
	Nodes        int      `json:"nodes"`
	Issues       []string `json:"issues,omitempty"`
	Error        string   `json:"error,omitempty"`
	Seconds      float64  `json:"seconds"`
}

// scoreCandidate compares one generated graph against the reference answer(s).
func scoreCandidate(a ask, refs []core.Graph, cand core.Graph, issues []core.LintIssue,
	manifests map[string]core.Manifest, genErr error) score {

	s := score{Num: a.Num, Ask: a.Title, Nodes: len(cand.Nodes)}
	if genErr != nil {
		s.Error = genErr.Error()
		return s
	}
	s.Generated = len(cand.Nodes) > 0
	// The gate the save path applies. Warnings are fine; errors mean a draft
	// the user cannot run.
	s.Valid = s.Generated && !hasLintError(issues)
	for _, is := range issues {
		s.Issues = append(s.Issues, fmt.Sprintf("%s [%s] %s", is.Severity, is.Code, is.Message))
	}

	wantApps := map[string]bool{}
	wantTrigger := ""
	for _, r := range refs {
		for app := range appsUsed(r, manifests) {
			wantApps[app] = true
		}
		if k := triggerKind(r, manifests); wantTrigger == "" || k != "manual" {
			wantTrigger = k
		}
	}
	gotApps := appsUsed(cand, manifests)

	s.TriggerWant = wantTrigger
	s.TriggerGot = triggerKind(cand, manifests)
	s.TriggerMatch = s.TriggerGot == s.TriggerWant

	for app := range wantApps {
		s.AppsWant = append(s.AppsWant, app)
		if !gotApps[app] {
			s.AppsMissing = append(s.AppsMissing, app)
		}
	}
	for app := range gotApps {
		if !wantApps[app] {
			s.AppsExtra = append(s.AppsExtra, app)
		}
	}
	sort.Strings(s.AppsWant)
	sort.Strings(s.AppsMissing)
	sort.Strings(s.AppsExtra)
	if len(s.AppsWant) == 0 {
		s.AppCoverage = 1
	} else {
		s.AppCoverage = float64(len(s.AppsWant)-len(s.AppsMissing)) / float64(len(s.AppsWant))
	}
	return s
}

// --- the offline harness check -------------------------------------------

// TestFlowGenScenariosHarness runs with no model and no key. It proves the corpus
// and the scorer are sound, so the eval can't rot between live runs: every ask
// in the document has a graph, and the scorer says a reference graph answers
// its own ask.
func TestFlowGenScenariosHarness(t *testing.T) {
	manifests := manifestMap()
	asks := loadAsks(t, scenariosDoc)
	refs := loadReferences(t, referenceDir)

	if len(asks) < 30 {
		t.Fatalf("parsed %d asks from %s — the corpus or its headings changed", len(asks), scenariosDoc)
	}
	seen := map[int]bool{}
	for _, a := range asks {
		if seen[a.Num] {
			t.Errorf("scenario %d appears twice", a.Num)
		}
		seen[a.Num] = true
		if a.Works == "" {
			t.Errorf("scenario %d (%q) has no 'It works when' line", a.Num, a.Title)
		}
		if len(refs[a.Num]) == 0 {
			t.Errorf("scenario %d (%q) has no reference graph under %s", a.Num, a.Title, referenceDir)
		}
		if p := a.Prompt(); len(p) < 40 {
			t.Errorf("scenario %d: prompt looks truncated: %q", a.Num, p)
		}
	}
	for n := range refs {
		if !seen[n] {
			t.Errorf("reference graph %02d-* has no scenario in %s", n, scenariosDoc)
		}
	}

	// The scorer must agree that the reference answers its own ask — if it
	// doesn't, the expectations are wrong and every live score is noise.
	for _, a := range asks {
		rs := refs[a.Num]
		// Score the union of the reference graphs against themselves by
		// treating the first as the candidate and checking the parts it owns.
		cand := rs[0]
		issues := core.ValidateGraphFull(cand, manifests)
		s := scoreCandidate(a, rs[:1], cand, issues, manifests, nil)
		if !s.Valid {
			t.Errorf("scenario %d: its own reference graph fails the gate: %v", a.Num, s.Issues)
		}
		if !s.TriggerMatch {
			t.Errorf("scenario %d: trigger kind not self-consistent (want %q got %q)", a.Num, s.TriggerWant, s.TriggerGot)
		}
		if s.AppCoverage != 1 {
			t.Errorf("scenario %d: reference doesn't cover its own apps, missing %v", a.Num, s.AppsMissing)
		}
	}

	t.Logf("corpus: %d asks, %d reference graphs across %d scenarios",
		len(asks), func() (n int) {
			for _, g := range refs {
				n += len(g)
			}
			return
		}(), len(refs))
	t.Logf("example prompt (scenario %d): %s", asks[0].Num, asks[0].Prompt())

	// And a wrong answer must score badly, or the eval flatters everything.
	wrong := core.Graph{Nodes: []core.Node{
		{ID: "n", Module: "render_text", Params: map[string]any{"template": "'x'"}},
	}}
	for _, a := range asks {
		if len(appsUsed(refs[a.Num][0], manifests)) == 0 {
			continue // a pure-transform scenario; nothing to miss
		}
		s := scoreCandidate(a, refs[a.Num], wrong, nil, manifests, nil)
		if s.AppCoverage == 1 {
			t.Errorf("scenario %d: an empty flow scored full app coverage", a.Num)
		}
	}
}

// --- the live eval --------------------------------------------------------

func TestFlowGenScenarios(t *testing.T) {
	key := os.Getenv("FLOWGEN_EVAL_KEY")
	if key == "" {
		key = os.Getenv("ANTHROPIC_API_KEY")
	}
	if key == "" {
		t.Skip("set FLOWGEN_EVAL_KEY (or ANTHROPIC_API_KEY) to run the generator eval — it calls a real model")
	}
	provider := os.Getenv("FLOWGEN_EVAL_PROVIDER")
	if provider == "" {
		provider = "claude"
	}
	outDir := os.Getenv("FLOWGEN_EVAL_OUT")
	if outDir == "" {
		outDir = t.TempDir()
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("report dir: %v", err)
	}

	manifests := manifestMap()
	mans := make([]core.Manifest, 0, len(manifests))
	for _, m := range manifests {
		mans = append(mans, m)
	}
	sort.Slice(mans, func(i, j int) bool { return mans[i].ID < mans[j].ID })

	asks := loadAsks(t, scenariosDoc)
	refs := loadReferences(t, referenceDir)
	if only := os.Getenv("FLOWGEN_EVAL_ONLY"); only != "" {
		want := map[int]bool{}
		for _, part := range strings.Split(only, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
				want[n] = true
			}
		}
		var filtered []ask
		for _, a := range asks {
			if want[a.Num] {
				filtered = append(filtered, a)
			}
		}
		asks = filtered
	}
	if len(asks) == 0 {
		t.Fatal("no scenarios selected")
	}

	scores := runScenarioEval(t, provider, key, asks, refs, manifests, mans, outDir)
	writeEvalReport(t, outDir, scores)

	// Report-only by default: a live model is not deterministic, so a hard
	// threshold belongs to whoever is tracking the number, not to the suite.
	if minValid := os.Getenv("FLOWGEN_EVAL_MIN_VALID"); minValid != "" {
		want, err := strconv.ParseFloat(minValid, 64)
		if err != nil {
			t.Fatalf("FLOWGEN_EVAL_MIN_VALID: %v", err)
		}
		valid := 0
		for _, s := range scores {
			if s.Valid {
				valid++
			}
		}
		got := 100 * float64(valid) / float64(len(scores))
		if got < want {
			t.Errorf("%.0f%% of drafts were valid, below the %.0f%% floor", got, want)
		}
	}
}

// writeEvalReport writes the machine-readable scores plus a summary table, and
// prints the table so a bare `go test -v` is enough to read the outcome.
func writeEvalReport(t *testing.T, dir string, scores []score) {
	t.Helper()
	if len(scores) == 0 {
		return
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].Num < scores[j].Num })

	b, _ := json.MarshalIndent(scores, "", "  ")
	jsonPath := filepath.Join(dir, "flowgen-eval.json")
	if err := os.WriteFile(jsonPath, b, 0o644); err != nil {
		t.Errorf("write report: %v", err)
	}

	var valid, triggers int
	var coverage, seconds float64
	var md strings.Builder
	md.WriteString("# Flow generator eval\n\n")
	md.WriteString("| # | Ask | Valid | Trigger | Apps reached | Missing |\n")
	md.WriteString("| --- | --- | --- | --- | --- | --- |\n")
	for _, s := range scores {
		if s.Valid {
			valid++
		}
		if s.TriggerMatch {
			triggers++
		}
		coverage += s.AppCoverage
		seconds += s.Seconds
		mark := func(ok bool) string {
			if ok {
				return "yes"
			}
			return "**no**"
		}
		missing := strings.Join(s.AppsMissing, ", ")
		if missing == "" {
			missing = "—"
		}
		md.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %.0f%% | %s |\n",
			s.Num, s.Ask, mark(s.Valid), mark(s.TriggerMatch), 100*s.AppCoverage, missing))
	}
	n := float64(len(scores))
	summary := fmt.Sprintf(
		"\n**%d scenarios** — %.0f%% produced a draft that passes the save gate, "+
			"%.0f%% picked the same kind of trigger, %.0f%% average app coverage, %.0fs average.\n",
		len(scores), 100*float64(valid)/n, 100*float64(triggers)/n, 100*coverage/n, seconds/n)
	md.WriteString(summary)

	mdPath := filepath.Join(dir, "flowgen-eval.md")
	if err := os.WriteFile(mdPath, []byte(md.String()), 0o644); err != nil {
		t.Errorf("write report: %v", err)
	}
	t.Logf("\n%s\nreport: %s", md.String(), mdPath)
}

// runScenarioEval generates one draft per ask and scores it. Shared by the
// live eval and by the scripted-model test below, so the path the live run
// takes is exercised in ordinary CI too.
func runScenarioEval(t *testing.T, provider, key string, asks []ask, refs map[int][]core.Graph,
	manifests map[string]core.Manifest, mans []core.Manifest, outDir string) []score {
	t.Helper()
	h := &HTTPGateway{} // no secrets wired: grounding is empty, as for a new org
	var scores []score

	for _, a := range asks {
		a := a
		t.Run(fmt.Sprintf("%02d", a.Num), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			start := time.Now()
			graph, issues, err := h.generateFlow(ctx, provider, key, a.Prompt(), mans,
				"evaltenant", "default", "Europe/Stockholm", nil)
			s := scoreCandidate(a, refs[a.Num], graph, issues, manifests, err)
			s.Seconds = time.Since(start).Seconds()
			scores = append(scores, s)

			// Keep the draft so a human can read what it actually built.
			if len(graph.Nodes) > 0 {
				b, _ := json.MarshalIndent(graph, "", "  ")
				_ = os.WriteFile(filepath.Join(outDir, fmt.Sprintf("%02d-generated.json", a.Num)), b, 0o644)
			}

			// A subtest failure names the scenario the generator couldn't do;
			// the run as a whole is judged by the summary.
			switch {
			case s.Error != "":
				t.Errorf("generator returned an error: %s", s.Error)
			case !s.Valid:
				t.Errorf("draft does not pass the save gate:\n  %s", strings.Join(s.Issues, "\n  "))
			}
			if !s.TriggerMatch {
				t.Logf("trigger: got %s, reference uses %s", s.TriggerGot, s.TriggerWant)
			}
			if len(s.AppsMissing) > 0 {
				t.Logf("apps not reached for: %v (reference uses %v)", s.AppsMissing, s.AppsWant)
			}
		})
	}
	return scores
}

// flowAsToolCall renders a graph the way the model emits one: a bare flow
// object, which the agent loop reads as an implicit emit.
func flowAsToolCall(g core.Graph) map[string]any {
	nodes := make([]any, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		nodes = append(nodes, node(n.ID, n.Module, n.Params))
	}
	edges := make([]any, 0, len(g.Edges))
	for _, e := range g.Edges {
		edges = append(edges, map[string]any{
			"from": e.From, "from_port": e.FromPort, "to": e.To, "to_port": e.ToPort,
		})
	}
	return map[string]any{"name": g.Name, "nodes": nodes, "edges": edges}
}

// TestFlowGenScenariosScripted drives the whole eval — generation, scoring,
// report — against a scripted model, so the live path can't rot unnoticed and
// the scoring is proven on GENERATED graphs, not just on the references
// scoring themselves.
func TestFlowGenScenariosScripted(t *testing.T) {
	manifests := manifestMap()
	mans := make([]core.Manifest, 0, len(manifests))
	for _, m := range manifests {
		mans = append(mans, m)
	}
	asks := loadAsks(t, scenariosDoc)
	refs := loadReferences(t, referenceDir)

	// Two scenarios: one the scripted model answers with the known-good graph,
	// one it answers with a plainly wrong flow.
	var good, bad ask
	for _, a := range asks {
		if a.Num == 2 && len(refs[2]) == 1 {
			good = a
		}
		if a.Num == 1 {
			bad = a
		}
	}
	if good.Num == 0 || bad.Num == 0 {
		t.Skip("scenarios 1 and 2 are no longer in the corpus")
	}

	// The scripted model answers in call order: the first scenario gets the
	// known-good graph, everything after it gets a flow that is structurally
	// fine but answers a different question — no apps, no trigger.
	notAsked := flowAsToolCall(core.Graph{
		Name:  "not what was asked",
		Nodes: []core.Node{{ID: "n", Module: "text", Params: map[string]any{"text": "hello"}}},
	})
	sp := &scriptedProvider{graphs: []map[string]any{flowAsToolCall(refs[good.Num][0]), notAsked}}
	llm.Register(llm.ProviderInfo{
		Name: "scripted-eval", Integration: "ScriptedEval", DefaultModel: "m", Provider: sp,
	})

	outDir := t.TempDir()
	scores := runScenarioEval(t, "scripted-eval", "no-key",
		[]ask{good, bad}, refs, manifests, mans, outDir)
	if len(scores) != 2 {
		t.Fatalf("scored %d scenarios, want 2", len(scores))
	}
	if sp.calls < 2 {
		t.Fatalf("the generator was called %d time(s) for 2 scenarios", sp.calls)
	}

	byNum := map[int]score{}
	for _, s := range scores {
		byNum[s.Num] = s
	}
	g := byNum[good.Num]
	if !g.Generated || !g.Valid {
		t.Errorf("the known-good answer should score valid: %+v", g)
	}
	if g.AppCoverage != 1 || !g.TriggerMatch {
		t.Errorf("the known-good answer should reach every app and the right trigger: %+v", g)
	}
	b := byNum[bad.Num]
	if b.AppCoverage == 1 {
		t.Errorf("an unrelated flow should not score full app coverage: %+v", b)
	}
	if len(b.AppsMissing) == 0 {
		t.Errorf("an unrelated flow should report the apps it never reached: %+v", b)
	}

	writeEvalReport(t, outDir, scores)
	for _, want := range []string{"flowgen-eval.json", "flowgen-eval.md"} {
		if _, err := os.Stat(filepath.Join(outDir, want)); err != nil {
			t.Errorf("report %s not written: %v", want, err)
		}
	}
	report, _ := os.ReadFile(filepath.Join(outDir, "flowgen-eval.md"))
	if !strings.Contains(string(report), "passes the save gate") {
		t.Errorf("summary line missing from the report:\n%s", report)
	}
}

// The system prompt is hand-written while the catalog is generated, so it is
// the part that silently goes stale as steps gain abilities. These are the
// facts a model cannot work out from the catalog rows alone, and that a third
// of the scenario corpus depends on: how a loop body reads the current item.
//
// Found by driving the generator by hand (see flowgen_manual_test.go): the
// guidance used to say "wire for_each.body into the per-item step's input",
// which is the documented footgun, and never mentioned ${item.…} at all.
func TestFlowGenPromptTeachesLoopBodies(t *testing.T) {
	prompt := flowGenSystemPrompt("(catalog omitted)")

	for _, must := range []string{
		"${item.field}",  // how a body step reads the current item
		"${item.}",       // the whole item
		"`pass` pin",     // where the body pin is wired
		"control pin",    // what the body pin is
		"real type",      // the whole-value handover, for structured params
		"unwrap_results", // collecting what the loop produced
	} {
		if !strings.Contains(prompt, must) {
			t.Errorf("the generator's guidance no longer mentions %q — a model can't build a loop without it", must)
		}
	}
	// The old wording pointed the control pin at a typed input, which injects
	// the whole row where a string was expected and the step rejects it.
	if strings.Contains(prompt, "for_each.body into the per-item step's input") {
		t.Error("the guidance tells the model to wire the body pin into a typed input — that is the footgun, not the pattern")
	}
}
