// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// Driving the flow generator by hand — no API key, no vendor call.
//
// manualProvider stands in for the model: each turn it writes the exact prompt
// the generator would have sent to a file and looks for a reply file written
// by whoever (or whatever) is playing the model. A missing reply stops the run
// with a message naming the file to write, so the loop can be walked one turn
// at a time and resumed.
//
// This exists for two reasons. It lets the eval run against any model — or any
// person — without wiring a vendor key into the test environment. And it makes
// the generator's own scaffolding inspectable: the prompt, the catalog rows and
// the describe_drop text are what the model actually has to work from, so
// reading them is how you find out whether they're enough.
//
//	FLOWGEN_MANUAL_DIR=.flowgen-manual go test ./daemon -run TestFlowGenManual -v
//
// Turn files live under <dir>/<scenario>/: turn-01-request.txt (what the model
// is shown), turn-01-reply.json (the tool call it answers with).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/llm"
)

// errNeedsReply stops the agent loop when the next turn has no answer yet.
type errNeedsReply struct{ path string }

func (e *errNeedsReply) Error() string {
	return "no reply for this turn — write the tool call to " + e.path
}

type manualProvider struct {
	dir   string
	turn  int
	stops []string // reply files that were missing, in order
}

func (p *manualProvider) Call(_ context.Context, _ string, req llm.Request) (llm.Result, *core.JobError) {
	p.turn++
	base := filepath.Join(p.dir, fmt.Sprintf("turn-%02d", p.turn))

	// Write what the model is shown, verbatim, so it can be read and answered.
	if err := os.WriteFile(base+"-request.txt", []byte(renderTurn(req)), 0o644); err != nil {
		return llm.Result{}, &core.JobError{Code: "io", Message: err.Error()}
	}
	replyPath := base + "-reply.json"
	data, err := os.ReadFile(replyPath)
	if err != nil {
		p.stops = append(p.stops, replyPath)
		return llm.Result{}, &core.JobError{Code: "needs_reply", Message: (&errNeedsReply{replyPath}).Error()}
	}
	var tool map[string]any
	if jerr := json.Unmarshal(data, &tool); jerr != nil {
		return llm.Result{}, &core.JobError{Code: "bad_reply",
			Message: fmt.Sprintf("%s is not valid JSON: %v", replyPath, jerr)}
	}
	return llm.Result{Tool: tool}, nil
}

// renderTurn flattens a request into the text a reader needs: the system
// prompt and catalog on turn one, then the running conversation.
func renderTurn(req llm.Request) string {
	var b strings.Builder
	if req.System != "" {
		b.WriteString("=== SYSTEM ===\n" + req.System + "\n\n")
	}
	if req.Tool != nil {
		schema, _ := json.MarshalIndent(req.Tool.Schema, "", "  ")
		b.WriteString("=== TOOL: " + req.Tool.Name + " ===\n" + req.Tool.Description + "\n" + string(schema) + "\n\n")
	}
	for i, m := range req.Messages {
		msg, _ := json.MarshalIndent(m, "", "  ")
		b.WriteString(fmt.Sprintf("=== MESSAGE %d ===\n%s\n\n", i+1, msg))
	}
	return b.String()
}

// TestFlowGenManual walks the generator for the scenarios named in
// FLOWGEN_MANUAL_ONLY (default: all of them), stopping at the first turn that
// has no reply yet. Re-run after writing the reply to continue.
func TestFlowGenManual(t *testing.T) {
	t.Parallel()
	dir := os.Getenv("FLOWGEN_MANUAL_DIR")
	if dir == "" {
		t.Skip("set FLOWGEN_MANUAL_DIR to drive the generator by hand (no API key needed)")
	}
	manifests := manifestMap()
	mans := make([]core.Manifest, 0, len(manifests))
	for _, m := range manifests {
		mans = append(mans, m)
	}
	sort.Slice(mans, func(i, j int) bool { return mans[i].ID < mans[j].ID })

	asks := loadAsks(t, scenariosDoc)
	refs := loadReferences(t, referenceDir)
	if only := os.Getenv("FLOWGEN_MANUAL_ONLY"); only != "" {
		want := map[int]bool{}
		for _, p := range strings.Split(only, ",") {
			if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
				want[n] = true
			}
		}
		var keep []ask
		for _, a := range asks {
			if want[a.Num] {
				keep = append(keep, a)
			}
		}
		asks = keep
	}
	if len(asks) == 0 {
		t.Fatal("no scenarios selected")
	}

	h := &HTTPGateway{}
	var scores []score
	for _, a := range asks {
		a := a
		t.Run(fmt.Sprintf("%02d", a.Num), func(t *testing.T) {
			sub := filepath.Join(dir, fmt.Sprintf("%02d", a.Num))
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			// Record the ask alongside the turns, so the folder is
			// self-contained for whoever answers it.
			_ = os.WriteFile(filepath.Join(sub, "ask.txt"), []byte(a.Prompt()+"\n"), 0o644)

			mp := &manualProvider{dir: sub}
			name := fmt.Sprintf("manual-%02d", a.Num)
			llm.Register(llm.ProviderInfo{Name: name, Integration: "Manual", DefaultModel: "manual", Provider: mp})

			graph, issues, err := h.flowAPI().generateFlow(context.Background(), name, "none", a.Prompt(), mans,
				"evaltenant", "default", "Europe/Stockholm", nil)
			if len(mp.stops) > 0 {
				t.Skipf("waiting for a reply: %s", mp.stops[len(mp.stops)-1])
			}
			s := scoreCandidate(a, refs[a.Num], graph, issues, manifests, err)
			scores = append(scores, s)
			if len(graph.Nodes) > 0 {
				b, _ := json.MarshalIndent(graph, "", "  ")
				_ = os.WriteFile(filepath.Join(sub, "generated.json"), b, 0o644)
			}
			if !s.Valid {
				t.Errorf("draft does not pass the save gate:\n  %s", strings.Join(s.Issues, "\n  "))
			}
			t.Logf("turns=%d valid=%v trigger=%s/%s apps=%.0f%% missing=%v",
				mp.turn, s.Valid, s.TriggerGot, s.TriggerWant, 100*s.AppCoverage, s.AppsMissing)
		})
	}
	if len(scores) > 0 {
		writeEvalReport(t, dir, scores)
	}
}
