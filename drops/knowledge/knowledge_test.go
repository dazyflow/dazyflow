// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package knowledge

import (
	"context"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/llm"
	_ "modernc.org/sqlite"
)

// A deterministic stand-in for a real embedding service: one dimension per
// vocabulary word, counted. Texts about the same thing land near each other,
// which is all the property under test needs — and it costs no network, no
// key and no cents.
var vocab = []string{"holiday", "leave", "invoice", "payment", "dog", "office"}

type fakeEmbedder struct {
	dims  int
	calls int
	seen  []string
	fail  *core.JobError
}

func (f *fakeEmbedder) Embed(_ context.Context, _ string, req llm.EmbedRequest) (llm.EmbedResult, *core.JobError) {
	if f.fail != nil {
		return llm.EmbedResult{}, f.fail
	}
	f.calls++
	f.seen = append(f.seen, req.Texts...)
	dims := f.dims
	if dims == 0 {
		dims = len(vocab)
	}
	out := make([][]float32, 0, len(req.Texts))
	for _, text := range req.Texts {
		v := make([]float32, dims)
		lower := strings.ToLower(text)
		for i := 0; i < dims && i < len(vocab); i++ {
			v[i] = float32(strings.Count(lower, vocab[i]))
		}
		// A dimension that is never zero, so a text with no vocabulary word
		// still has a direction rather than being the zero vector.
		v[dims-1] += 0.1
		out = append(out, v)
	}
	return llm.EmbedResult{Vectors: out, Model: req.Model}, nil
}

func useFake(t *testing.T, f *fakeEmbedder) {
	t.Helper()
	llm.Register(llm.ProviderInfo{
		Name:              "fake",
		Integration:       "Fake",
		Provider:          nil,
		Embedder:          f,
		DefaultEmbedModel: "fake-embed",
	})
}

func conn(extra map[string]any) map[string]any {
	p := map[string]any{"provider": "Fake", "api_key": "k"}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func add(t *testing.T, root string, p map[string]any, text, source string) core.Result {
	t.Helper()
	in := map[string]core.Ref{"text": {MIME: "text/plain", Inline: text}}
	if source != "" {
		in["source"] = core.Ref{MIME: "text/plain", Inline: source}
	}
	res, err := executeAdd(t.Context(), core.Job{ID: "t", WorkspaceRoot: root, Params: p, Input: in}, nil)
	if err != nil {
		t.Fatalf("executeAdd: %v", err)
	}
	return res
}

func find(t *testing.T, root string, p map[string]any, query string) core.Result {
	t.Helper()
	res, err := executeSearch(t.Context(), core.Job{ID: "t", WorkspaceRoot: root, Params: p,
		Input: map[string]core.Ref{"query": {MIME: "text/plain", Inline: query}}}, nil)
	if err != nil {
		t.Fatalf("executeSearch: %v", err)
	}
	return res
}

func okResult(t *testing.T, res core.Result) core.Result {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	return res
}

func failsWith(t *testing.T, res core.Result, code, says string) {
	t.Helper()
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want an error", res.Status)
	}
	if res.Error.Code != code {
		t.Fatalf("code=%q (%s), want %q", res.Error.Code, res.Error.Message, code)
	}
	if says != "" && !strings.Contains(res.Error.Message, says) {
		t.Errorf("message = %q, want it to mention %q", res.Error.Message, says)
	}
}

const handbook = `Holiday. Every employee gets twenty-five days of holiday a year, and holiday
carries over until March.

Invoice handling. An invoice is paid thirty days after it arrives, and payment
goes out on a Friday.

The office. The office dog is called Rutger and the office is open from seven.`

func TestKnowledge_FindsThePassageThatBearsOnTheQuestion(t *testing.T) {
	useFake(t, &fakeEmbedder{})
	root := t.TempDir()

	added := okResult(t, add(t, root, conn(map[string]any{"base": "hb", "chunk_size": 120, "overlap": 20}), handbook, "handbook.md"))
	if n, _ := added.Output["chunks"].Inline.(int); n < 2 {
		t.Fatalf("stored %v passages, want the document split up", added.Output["chunks"].Inline)
	}
	if got := added.Output["source"].Inline; got != "handbook.md" {
		t.Errorf("source = %v", got)
	}

	res := okResult(t, find(t, root, conn(map[string]any{"base": "hb", "limit": 1}), "how much holiday do I get"))
	text, _ := res.Output["text"].Inline.(string)
	if !strings.Contains(strings.ToLower(text), "holiday") {
		t.Errorf("passages = %q, want the holiday one first", text)
	}
	rows, _ := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want one", rows)
	}
	if rows[0]["source"] != "handbook.md" {
		t.Errorf("row source = %v", rows[0]["source"])
	}
	if score, _ := rows[0]["score"].(float64); score <= 0 || score > 1.0001 {
		t.Errorf("score = %v, want a cosine between 0 and 1", rows[0]["score"])
	}
	if n, _ := res.Output["count"].Inline.(int); n != 1 {
		t.Errorf("count = %v", res.Output["count"].Inline)
	}
}

// Re-reading the same document must refresh it, not pile up a second copy.
func TestKnowledge_ReAddingASourceReplacesIt(t *testing.T) {
	useFake(t, &fakeEmbedder{})
	root := t.TempDir()
	p := conn(map[string]any{"base": "hb", "chunk_size": 120, "overlap": 20})

	first := okResult(t, add(t, root, p, handbook, "handbook.md"))
	second := okResult(t, add(t, root, p, handbook, "handbook.md"))
	if first.Output["chunks"].Inline != second.Output["chunks"].Inline {
		t.Fatalf("second add stored %v, first stored %v", second.Output["chunks"].Inline, first.Output["chunks"].Inline)
	}

	res := okResult(t, find(t, root, conn(map[string]any{"base": "hb", "limit": 50}), "holiday"))
	rows, _ := res.Output["rows"].Inline.([]map[string]any)
	if want, _ := first.Output["chunks"].Inline.(int); len(rows) != want {
		t.Errorf("base holds %d passages after two identical adds, want %d", len(rows), want)
	}
}

// With no source of its own, the text's fingerprint identifies it — so the
// same text twice is stored once, and different texts still coexist.
func TestKnowledge_FingerprintsAnUnnamedDocument(t *testing.T) {
	useFake(t, &fakeEmbedder{})
	root := t.TempDir()
	p := conn(map[string]any{"base": "hb"})

	one := okResult(t, add(t, root, p, "The office dog is called Rutger.", ""))
	if src, _ := one.Output["source"].Inline.(string); !strings.HasPrefix(src, "text:") {
		t.Errorf("source = %v, want a fingerprint", one.Output["source"].Inline)
	}
	okResult(t, add(t, root, p, "The office dog is called Rutger.", ""))
	okResult(t, add(t, root, p, "An invoice is paid after thirty days.", ""))

	rows, _ := okResult(t, find(t, root, conn(map[string]any{"base": "hb", "limit": 50}), "dog")).
		Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 {
		t.Errorf("base holds %d passages, want 2 — the repeat should have replaced itself", len(rows))
	}
}

func TestKnowledge_LimitAndFloor(t *testing.T) {
	useFake(t, &fakeEmbedder{})
	root := t.TempDir()
	p := conn(map[string]any{"base": "hb", "chunk_size": 120, "overlap": 20})
	okResult(t, add(t, root, p, handbook, "handbook.md"))

	all := okResult(t, find(t, root, conn(map[string]any{"base": "hb", "limit": 50}), "holiday"))
	total, _ := all.Output["count"].Inline.(int)
	if total < 3 {
		t.Fatalf("only %d passages to work with", total)
	}

	capped := okResult(t, find(t, root, conn(map[string]any{"base": "hb", "limit": 2}), "holiday"))
	if n, _ := capped.Output["count"].Inline.(int); n != 2 {
		t.Errorf("count = %v with limit 2", capped.Output["count"].Inline)
	}

	// Nothing scores a perfect 1 against a query it does not contain, so a
	// floor that high must empty the result.
	strict := okResult(t, find(t, root, conn(map[string]any{"base": "hb", "min_score": 0.999}), "payment"))
	if n, _ := strict.Output["count"].Inline.(int); n != 0 {
		t.Errorf("count = %v with a 0.999 floor, want 0", strict.Output["count"].Inline)
	}
	if text, _ := strict.Output["text"].Inline.(string); text != "" {
		t.Errorf("passages = %q, want empty", text)
	}
}

// A base built by one model cannot be queried by another — the numbers are
// not comparable, and saying so beats ranking by noise.
func TestKnowledge_RefusesToMixModels(t *testing.T) {
	useFake(t, &fakeEmbedder{})
	root := t.TempDir()
	okResult(t, add(t, root, conn(map[string]any{"base": "hb"}), handbook, "handbook.md"))

	other := conn(map[string]any{"base": "hb", "model": "another-embed"})
	failsWith(t, add(t, root, other, handbook, "handbook.md"), "bad_param", "cannot be compared")
	failsWith(t, find(t, root, other, "holiday"), "bad_param", "different model")
}

func TestKnowledge_SearchNeedsABaseThatExists(t *testing.T) {
	useFake(t, &fakeEmbedder{})
	root := t.TempDir()

	// Nothing added at all: no store file yet.
	failsWith(t, find(t, root, conn(map[string]any{"base": "hb"}), "holiday"), "no_such_base", "hb")

	// A store with a different base in it.
	okResult(t, add(t, root, conn(map[string]any{"base": "prices"}), "A coffee is thirty kronor.", "prices"))
	failsWith(t, find(t, root, conn(map[string]any{"base": "hb"}), "holiday"), "no_such_base", "hb")
}

func TestKnowledge_Refusals(t *testing.T) {
	useFake(t, &fakeEmbedder{})
	root := t.TempDir()

	failsWith(t, add(t, root, conn(map[string]any{}), handbook, "x"), "bad_param", "Knowledge base")
	failsWith(t, add(t, root, conn(map[string]any{"base": "hb"}), "   ", "x"), "bad_input", "no text")
	failsWith(t, find(t, root, conn(map[string]any{"base": "hb"}), "  "), "bad_input", "no question")

	// Not connected at all, and connected to something that cannot embed.
	failsWith(t, add(t, root, map[string]any{"base": "hb"}, handbook, "x"), "not_connected", "Apps page")
	failsWith(t, add(t, root, map[string]any{"base": "hb", "provider": "Claude", "api_key": "k"}, handbook, "x"),
		"bad_param", "cannot make embeddings")
	failsWith(t, add(t, root, map[string]any{"base": "hb", "provider": "Fake"}, handbook, "x"),
		"not_connected", "no API key")
}

// The service failing is the step failing, with the service's own words.
func TestKnowledge_PassesTheServicesErrorOn(t *testing.T) {
	useFake(t, &fakeEmbedder{fail: &core.JobError{Code: "llm_auth", Message: "ChatGPT rejected the API key"}})
	res := add(t, t.TempDir(), conn(map[string]any{"base": "hb"}), handbook, "x")
	failsWith(t, res, "llm_auth", "rejected the API key")
}

// Embedding is billed per request, so a long document must not become one
// request per passage.
func TestKnowledge_EmbedsInBatches(t *testing.T) {
	f := &fakeEmbedder{}
	useFake(t, f)
	long := strings.Repeat("The office dog is called Rutger and the office is open from seven. ", 400)

	okResult(t, add(t, t.TempDir(), conn(map[string]any{"base": "hb", "chunk_size": 100, "overlap": 0}), long, "long"))
	if len(f.seen) < embedBatch+1 {
		t.Fatalf("only %d passages, not enough to prove batching", len(f.seen))
	}
	wantCalls := (len(f.seen) + embedBatch - 1) / embedBatch
	if f.calls != wantCalls {
		t.Errorf("%d requests for %d passages, want %d", f.calls, len(f.seen), wantCalls)
	}
}

func TestKnowledge_VerifierChecksTheServiceAnswers(t *testing.T) {
	useFake(t, &fakeEmbedder{})
	if err := verifyConnection(t.Context(), map[string]string{"provider": "Fake", "api_key": "k"}); err != nil {
		t.Errorf("a working connection failed to verify: %v", err)
	}
	if err := verifyConnection(t.Context(), map[string]string{}); err == nil {
		t.Error("an unconfigured connection verified")
	}
	if err := verifyConnection(t.Context(), map[string]string{"provider": "Fake"}); err == nil {
		t.Error("a connection with no key verified")
	}
	useFake(t, &fakeEmbedder{fail: &core.JobError{Code: "llm_auth", Message: "nope"}})
	if err := verifyConnection(t.Context(), map[string]string{"provider": "Fake", "api_key": "bad"}); err == nil {
		t.Error("a rejected key verified")
	}
}
