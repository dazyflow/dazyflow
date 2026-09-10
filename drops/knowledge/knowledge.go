// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package knowledge is retrieval: put documents somewhere a flow can ask them
// questions, and get back the handful of passages that bear on a question.
//
// Two steps, not six. Embeddings need a provider's credentials, and a drop
// only ever receives the connection named by its own manifest (see
// engine/secrets.go) — so a per-provider pair would mean six catalogue
// entries. Instead the provider is part of a "Knowledge" connection, chosen
// once for the workspace. That also buys the correctness this needs for free:
// vectors from two different models occupy different spaces and comparing
// them is meaningless, so a base records what built it and refuses a query
// embedded any other way.
package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/llm"
)

const integration = "Knowledge"

// embedBatch is how many pieces of text go up in one request. Every provider
// here bills and rate-limits per request, so batching is cheaper; the ceiling
// keeps one request from carrying a whole book.
const embedBatch = 64

// knowledgeProviders are the integrations offered on the connection. Held by
// hand rather than read from the provider registry at init(), because package
// initialization order does not promise the providers have registered yet —
// TestConnectionOffersEveryEmbeddingProvider is what keeps the two in step.
var knowledgeProviders = []string{"ChatGPT", "Gemini", "Ollama"}

func connectionFields() []core.ConnectionField {
	return []core.ConnectionField{
		{
			Key: "provider", Label: "Embeddings from", Required: true,
			Options: knowledgeProviders,
			Help:    "Who turns your text into numbers. Whichever you pick, every knowledge base is built with it — switching later means building the bases again.",
		},
		{
			Key: "api_key", Label: "API key", Secret: true,
			Placeholder: "sk-… / AIza…",
			Help:        "The key for the service above. Leave it empty for Ollama, which runs on your own machine.",
		},
		{
			Key: "model", Label: "Embedding model",
			Placeholder: "text-embedding-3-small",
			Help:        "Leave empty for the provider's default. Changing it means building your bases again — a base remembers the model that made it.",
		},
		{
			Key: "base_url", Label: "Service address",
			Placeholder: "http://localhost:11434",
			Help:        "Only for Ollama, or a proxy in front of one of the others. A localhost address also needs DAZYFLOW_ALLOW_PRIVATE_EGRESS on the daemon.",
		},
	}
}

// connectionParams mirrors the connection onto the step, the way the AI steps
// do: injected automatically, overridable per step, hidden behind Advanced.
const connectionParams = `
	"provider":{"type":"string","x_advanced":true,"description":"Injected from the Knowledge connection — leave unset."},
	"api_key":{"type":"string","x_advanced":true,"description":"Injected from the Knowledge connection — leave unset."},
	"model":{"type":"string","x_advanced":true,"description":"Injected from the Knowledge connection — leave unset."},
	"base_url":{"type":"string","x_advanced":true,"description":"Injected from the Knowledge connection — leave unset."},
	"timeout_ms":{"type":"integer","default":60000,"x_advanced":true,"description":"How long to wait for the embedding service, in milliseconds."}`

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "knowledge_add",
			Version:     "1.0",
			Label:       "Knowledge",
			Subtitle:    "Add documents",
			Color:       "#7c5cff",
			Icon:        "file-input",
			Category:    "io",
			Provider:    "internal",
			Integration: integration,
			Tags: []string{"knowledge", "rag", "embedding", "index", "documents",
				"search", "retrieval", "vector", "semantic", "learn", "ingest"},
			Description: "Put a document where your flows can ask it questions. The text is cut into passages, each " +
				"passage is turned into numbers that capture what it is about, and those go into a knowledge base " +
				"in this workspace.\n\n" +
				"Name the base after what is in it — handbook, prices, policies. Anything can feed it: a page read " +
				"with Read a web page, a PDF's text, rows from a collection, an email body.\n\n" +
				"'Where it came from' is how a document is identified. Add the same source again and its passages " +
				"are REPLACED, not duplicated, so a nightly re-read keeps the base current instead of growing a " +
				"second copy of everything. Leave it empty and the text's own fingerprint is used, which means " +
				"adding the identical document twice changes nothing.\n\n" +
				"Connect Knowledge once on the Apps page to say who makes the embeddings. A base remembers the " +
				"model that built it and will not mix two — numbers from different models are not comparable, so " +
				"changing the model means building the base again.",
			Summary: "Chunk a document, embed it, and store it in a workspace knowledge base for later retrieval.",
			Examples: []core.ParamsExample{
				{
					Title:  "Index the company handbook",
					Params: json.RawMessage(`{"base":"handbook","source":"handbook.md"}`),
					Notes:  "Wire the text into 'text'. Re-running replaces that source's passages rather than duplicating them.",
				},
				{
					Title:  "Keep a page up to date every night",
					Params: json.RawMessage(`{"base":"prices","source":"https://shop.example.com/prices"}`),
					Notes:  "Schedule → Web request → Read a web page → here. The source names the page, so each run refreshes it.",
				},
				{
					Title:  "Longer passages, for prose that argues over paragraphs",
					Params: json.RawMessage(`{"base":"policies","chunk_size":2000,"overlap":300}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "text", Label: "Text", Required: true, MIME: []string{"text/plain"}},
				{Port: "source", Label: "Where it came from", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "chunks", Label: "Passages stored", MIME: []string{"application/json"}},
				{Port: "source", Label: "Where it came from", MIME: []string{"text/plain"}},
			},
			ParamsSchema: []byte(`{
				"type":"object",
				"properties":{
					"base":{"type":"string","title":"Knowledge base","description":"What this collection of documents is called — handbook, prices, policies. Made the first time you add to it."},
					"source":{"type":"string","title":"Where it came from","description":"What identifies this document — a filename, a URL, a ticket number. Adding the same source again replaces its passages. Empty means the text's own fingerprint, so the identical document twice is stored once."},
					"chunk_size":{"type":"integer","default":1200,"minimum":100,"maximum":8000,"title":"Passage length","x_advanced":true,"description":"How many characters a passage may hold. Longer keeps an argument together; shorter makes a search point at the exact sentence."},
					"overlap":{"type":"integer","default":150,"minimum":0,"title":"Overlap","x_advanced":true,"description":"How many characters each passage repeats from the one before, so a fact split across a seam survives whole in one of them."},` +
				connectionParams + `
				},
				"required":["base"]
			}`),
			ConnectionFields: connectionFields(),
			Idempotent:       true,
		},
		Execute: executeAdd,
	})

	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "knowledge_search",
			Version:     "1.0",
			Label:       "Knowledge",
			Subtitle:    "Find related",
			Color:       "#7c5cff",
			Icon:        "file-search",
			Category:    "io",
			Provider:    "internal",
			Integration: integration,
			Tags: []string{"knowledge", "rag", "search", "retrieval", "related", "similar",
				"semantic", "question", "answer", "context", "embedding", "vector"},
			Description: "Find the passages in a knowledge base that bear on a question, and hand them to an AI step " +
				"to answer from.\n\n" +
				"This is the retrieval half of asking your own documents questions. Wire the question into 'query'; " +
				"the passages come back on 'text', already joined and ready to paste into a prompt, and on 'rows' " +
				"one per passage with the score and where it came from. Then wire 'text' into ChatGPT or Claude " +
				"alongside the question — \"answer using only this\" — and the answer is grounded in your own " +
				"documents instead of the model's memory. Wire 'text' into the AI step's Prompt and put the " +
				"question in its System prompt, or write one prompt that reads both with " +
				"${upstream.<this step's id>.text}.\n\n" +
				"It matches by meaning rather than by words: \"how much holiday do I get\" finds the passage about " +
				"annual leave. Ask for more passages when answers come out thin, fewer when they come out vague. " +
				"'Least score' drops the weak matches — around 0.3 is a reasonable floor for most models, and " +
				"nothing at all is better than a passage about the wrong thing.\n\n" +
				"The base has to have been built with the same embedding model this connection uses; if it was not, " +
				"the step says so rather than returning nonsense.",
			Summary: "Retrieve the passages closest in meaning to a question, as joined text ready for a prompt and as scored rows.",
			Examples: []core.ParamsExample{
				{
					Title:  "Answer from the handbook",
					Params: json.RawMessage(`{"base":"handbook","limit":5}`),
					Notes:  "Wire 'text' into an AI step's prompt together with the question.",
				},
				{
					Title:  "Only strong matches",
					Params: json.RawMessage(`{"base":"policies","limit":3,"min_score":0.35}`),
					Notes:  "Nothing above the floor comes back empty, which a Branch can turn into \"I don't know\".",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "query", Label: "Question", Required: true, MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "text", Label: "Passages", MIME: []string{"text/plain"}},
				{Port: "rows", Label: "Matches", MIME: []string{"application/json"}},
				{Port: "count", Label: "How many", MIME: []string{"application/json"}},
			},
			ParamsSchema: []byte(`{
				"type":"object",
				"properties":{
					"base":{"type":"string","title":"Knowledge base","description":"Which collection of documents to search."},
					"limit":{"type":"integer","default":5,"minimum":1,"maximum":50,"title":"How many passages","description":"How many of the closest passages to bring back. Five is a good starting point for a prompt."},
					"min_score":{"type":"number","default":0,"minimum":0,"maximum":1,"title":"Least score","description":"Drop matches weaker than this. 0 keeps everything; around 0.3 keeps only passages that are really about the question."},
					"separator":{"type":"string","default":"\n\n---\n\n","title":"Between passages","x_advanced":true,"description":"What goes between the passages on the joined 'Passages' output."},` +
				connectionParams + `
				},
				"required":["base"]
			}`),
			ConnectionFields: connectionFields(),
			Idempotent:       true,
		},
		Execute: executeSearch,
	})

	engine.RegisterConnectionVerifier(integration, verifyConnection)
}

// settings is the connection, read off the params the engine injected.
type settings struct {
	info      llm.ProviderInfo
	apiKey    string
	model     string
	baseURL   string
	timeoutMS int
}

func readSettings(job core.Job) (settings, *core.Result) {
	name := strings.TrimSpace(params.StringDefault(job.Params, "provider", ""))
	if name == "" {
		r := params.Err(job, "not_connected",
			"Knowledge is not connected yet — open the Apps page, pick who makes the embeddings, and paste the key")
		return settings{}, &r
	}
	info, ok := llm.EmbedderByIntegration(name)
	if !ok {
		r := params.Err(job, "bad_param", fmt.Sprintf(
			"%q cannot make embeddings — pick one of %s on the Knowledge connection",
			name, strings.Join(knowledgeProviders, ", ")))
		return settings{}, &r
	}
	s := settings{
		info:      info,
		apiKey:    strings.TrimSpace(params.StringDefault(job.Params, "api_key", "")),
		model:     strings.TrimSpace(params.StringDefault(job.Params, "model", "")),
		baseURL:   strings.TrimSpace(params.StringDefault(job.Params, "base_url", "")),
		timeoutMS: params.IntDefault(job.Params, "timeout_ms", 60000),
	}
	if s.model == "" {
		s.model = info.DefaultEmbedModel
	}
	// Ollama runs locally and needs no key; the cloud services do, and saying
	// so here beats a 401 from the vendor.
	if s.apiKey == "" && info.Name != "ollama" {
		r := params.Err(job, "not_connected",
			"no API key for "+info.Integration+" — add it to the Knowledge connection on the Apps page")
		return settings{}, &r
	}
	return s, nil
}

// embedAll turns text into unit vectors, in batches.
func embedAll(ctx context.Context, s settings, texts []string) ([][]float32, *core.JobError) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += embedBatch {
		end := min(start+embedBatch, len(texts))
		res, jerr := llm.Embed(ctx, s.info.Name, s.apiKey, llm.EmbedRequest{
			Model:     s.model,
			Texts:     texts[start:end],
			BaseURL:   s.baseURL,
			TimeoutMS: s.timeoutMS,
		})
		if jerr != nil {
			return nil, jerr
		}
		for _, v := range res.Vectors {
			if len(v) == 0 {
				return nil, &core.JobError{Code: "llm_api", Message: "the embedding service returned an empty vector"}
			}
			out = append(out, normalize(v))
		}
	}
	return out, nil
}

func executeAdd(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	base := strings.TrimSpace(params.StringDefault(job.Params, "base", ""))
	if base == "" {
		return params.Err(job, "bad_param", "'Knowledge base' is required — name the collection these documents go in"), nil
	}
	text, ok := params.TextInputOr(job, "text", params.StringDefault(job.Params, "text", ""))
	if !ok {
		return params.Err(job, "bad_input", "input 'text' must be text"), nil
	}
	if strings.TrimSpace(text) == "" {
		return params.Err(job, "bad_input", "there is no text to add"), nil
	}
	source, _ := params.TextInputOr(job, "source", strings.TrimSpace(params.StringDefault(job.Params, "source", "")))
	source = strings.TrimSpace(source)
	if source == "" {
		source = fingerprint(text)
	}

	set, errRes := readSettings(job)
	if errRes != nil {
		return *errRes, nil
	}

	pieces := splitText(text,
		params.IntDefault(job.Params, "chunk_size", defaultChunkSize),
		params.IntDefault(job.Params, "overlap", defaultOverlap))
	if len(pieces) == 0 {
		return params.Err(job, "bad_input", "there is no text to add"), nil
	}

	db, errRes := openStore(job, true)
	if errRes != nil {
		return *errRes, nil
	}
	defer db.Close()

	// A base is built by one model. Refusing here is the difference between a
	// search that says "rebuild this" and one that quietly ranks by noise.
	existing, found, err := readBase(ctx, db, base)
	if err != nil {
		return params.Err(job, "db", err.Error()), nil
	}
	if found && (existing.provider != set.info.Name || existing.model != set.model) {
		return params.Err(job, "bad_param", fmt.Sprintf(
			"the %q base was built with %s/%s and this connection uses %s/%s — numbers from two models cannot be compared. Use the original model, or start the base again under a new name",
			base, existing.provider, existing.model, set.info.Name, set.model)), nil
	}

	held, err := countChunks(ctx, db, base)
	if err != nil {
		return params.Err(job, "db", err.Error()), nil
	}
	if held+len(pieces) > maxChunks {
		return params.Err(job, "too_large", fmt.Sprintf(
			"the %q base would hold %d passages, past the %d limit — split it into more bases, or store longer passages",
			base, held+len(pieces), maxChunks)), nil
	}

	params.EmitProgress(progress, job, 0.2, fmt.Sprintf("embedding %d passages", len(pieces)))
	vectors, jerr := embedAll(ctx, set, pieces)
	if jerr != nil {
		return core.Result{JobID: job.ID, Status: core.StatusError, Error: jerr}, nil
	}

	chunks := make([]chunk, len(pieces))
	for i := range pieces {
		chunks[i] = chunk{source: source, ord: i, text: pieces[i], vector: vectors[i]}
	}
	info := baseInfo{provider: set.info.Name, model: set.model, dim: len(vectors[0])}
	if err := replaceSource(ctx, db, base, info, source, chunks); err != nil {
		return params.Err(job, "db", err.Error()), nil
	}

	params.EmitProgress(progress, job, 1, fmt.Sprintf("stored %d passages", len(chunks)))
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"chunks": {MIME: "application/json", Inline: len(chunks)},
			"source": {MIME: "text/plain", Inline: source},
		},
	}, nil
}

func executeSearch(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	base := strings.TrimSpace(params.StringDefault(job.Params, "base", ""))
	if base == "" {
		return params.Err(job, "bad_param", "'Knowledge base' is required — name the collection to search"), nil
	}
	query, ok := params.TextInputOr(job, "query", params.StringDefault(job.Params, "query", ""))
	if !ok || strings.TrimSpace(query) == "" {
		return params.Err(job, "bad_input", "there is no question to search for"), nil
	}

	set, errRes := readSettings(job)
	if errRes != nil {
		return *errRes, nil
	}

	db, errRes := openStore(job, false)
	if errRes != nil {
		return *errRes, nil
	}
	if db == nil {
		return params.Err(job, "no_such_base", fmt.Sprintf(
			"there is no knowledge base called %q yet — add documents to one first", base)), nil
	}
	defer db.Close()

	info, found, err := readBase(ctx, db, base)
	if err != nil {
		return params.Err(job, "db", err.Error()), nil
	}
	if !found {
		return params.Err(job, "no_such_base", fmt.Sprintf(
			"there is no knowledge base called %q — check the name, or add documents to it first", base)), nil
	}
	if info.provider != set.info.Name || info.model != set.model {
		return params.Err(job, "bad_param", fmt.Sprintf(
			"the %q base was built with %s/%s and this connection uses %s/%s — a question embedded by a different model cannot be compared against it",
			base, info.provider, info.model, set.info.Name, set.model)), nil
	}

	vectors, jerr := embedAll(ctx, set, []string{query})
	if jerr != nil {
		return core.Result{JobID: job.ID, Status: core.StatusError, Error: jerr}, nil
	}

	hits, err := search(ctx, db, base, vectors[0],
		params.ClampInt(params.IntDefault(job.Params, "limit", 5), 1, 50),
		floatParam(job.Params, "min_score"))
	if err != nil {
		return params.Err(job, "db", err.Error()), nil
	}

	rows := make([]map[string]any, 0, len(hits))
	texts := make([]string, 0, len(hits))
	for _, h := range hits {
		rows = append(rows, map[string]any{"text": h.Text, "source": h.Source, "score": h.Score})
		texts = append(texts, h.Text)
	}
	separator := params.StringDefault(job.Params, "separator", "\n\n---\n\n")

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"text":  {MIME: "text/plain", Inline: strings.Join(texts, separator)},
			"rows":  {MIME: "application/json", Inline: rows, Headers: []string{"text", "source", "score"}},
			"count": {MIME: "application/json", Inline: len(rows)},
		},
	}, nil
}

// verifyConnection embeds one short string, so "Connected" means the key, the
// model name and the address all work together — not merely that a key was
// pasted.
func verifyConnection(ctx context.Context, conn map[string]string) error {
	name := strings.TrimSpace(conn["provider"])
	if name == "" {
		return fmt.Errorf("pick who makes the embeddings — %s", strings.Join(knowledgeProviders, ", "))
	}
	info, ok := llm.EmbedderByIntegration(name)
	if !ok {
		return fmt.Errorf("%q cannot make embeddings", name)
	}
	key := strings.TrimSpace(conn["api_key"])
	if key == "" && info.Name != "ollama" {
		return fmt.Errorf("no API key — paste your %s key", info.Integration)
	}
	model := strings.TrimSpace(conn["model"])
	if model == "" {
		model = info.DefaultEmbedModel
	}
	res, jerr := llm.Embed(ctx, info.Name, key, llm.EmbedRequest{
		Model:     model,
		Texts:     []string{"dazyflow connection check"},
		BaseURL:   strings.TrimSpace(conn["base_url"]),
		TimeoutMS: 20000,
	})
	if jerr != nil {
		return fmt.Errorf("%s", jerr.Message)
	}
	if len(res.Vectors) != 1 || len(res.Vectors[0]) == 0 {
		return fmt.Errorf("%s answered without an embedding — check the model name", info.Integration)
	}
	return nil
}

// fingerprint identifies a document by its content, so adding the same text
// twice with no source of its own stores it once.
func fingerprint(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "text:" + hex.EncodeToString(sum[:8])
}

func floatParam(p map[string]any, key string) float64 {
	switch v := p[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	}
	return 0
}

// Forgetting needs no embeddings, so this step declares no connection fields —
// the only step in the integration that works before Knowledge is connected.
func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "knowledge_forget",
			Version:     "1.0",
			Label:       "Knowledge",
			Subtitle:    "Forget a document",
			Color:       "#7c5cff",
			Icon:        "trash-2",
			Category:    "io",
			Provider:    "internal",
			Integration: integration,
			Tags: []string{"knowledge", "forget", "delete", "remove", "rag",
				"document", "stale", "clean up", "tidy", "empty", "rebuild"},
			Description: "Take a document back out of a knowledge base. Adding a source again replaces its " +
				"passages, so this is for the other case — the page that no longer exists, the policy that was " +
				"withdrawn, the customer who asked to be forgotten.\n\n" +
				"Name the same 'Where it came from' the document was added under. Every passage that came from it " +
				"goes; the rest of the base is untouched.\n\n" +
				"Emptying a whole base is deliberately harder: leave the source empty and the step refuses unless " +
				"you also turn on 'Forget the whole base'. That frees the name to be built again — with a " +
				"different embedding model, if that is why you are clearing it.\n\n" +
				"It needs no connection: forgetting reads no embeddings, so this works even before Knowledge is " +
				"set up.",
			Summary: "Remove one document's passages from a knowledge base, or empty the base entirely.",
			Examples: []core.ParamsExample{
				{
					Title:  "Forget a page that no longer exists",
					Params: json.RawMessage(`{"base":"prices","source":"https://shop.example.com/old-page"}`),
				},
				{
					Title:  "Start a base again under a new model",
					Params: json.RawMessage(`{"base":"handbook","all":true}`),
					Notes:  "Frees the name: the base no longer remembers which model built it.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "source", Label: "Where it came from", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "forgotten", Label: "Passages forgotten", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"base":{"type":"string","title":"Knowledge base","description":"Which collection of documents to take it out of."},
					"source":{"type":"string","title":"Where it came from","description":"The document to forget, named exactly as it was added — a filename, a URL, a ticket number. Leave it empty only to empty the whole base."},
					"all":{"type":"boolean","default":false,"title":"Forget the whole base","description":"Confirms that a step with no document named really is meant to empty the base. Ignored when a document is named."}
				},
				"required":["base"]
			}`),
			// Forgetting twice removes nothing the second time, so a retry edge
			// is safe.
			Idempotent: true,
		},
		Execute: executeForget,
	})
}

func executeForget(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	base := strings.TrimSpace(params.StringDefault(job.Params, "base", ""))
	if base == "" {
		return params.Err(job, "bad_param", "'Knowledge base' is required — name the collection to take it out of"), nil
	}
	source, _ := params.TextInputOr(job, "source", strings.TrimSpace(params.StringDefault(job.Params, "source", "")))
	source = strings.TrimSpace(source)
	all := params.BoolDefault(job.Params, "all", false)
	if source == "" && !all {
		return params.Err(job, "bad_param",
			"this would empty the whole "+base+" base — name the document under 'Where it came from', or turn on 'Forget the whole base' to say you meant it"), nil
	}

	db, errRes := openStore(job, false)
	if errRes != nil {
		return *errRes, nil
	}
	// Nothing was ever added, so there is nothing to forget.
	if db == nil {
		return forgottenResult(job, 0), nil
	}
	defer db.Close()

	var (
		n   int
		err error
	)
	if source == "" {
		n, err = forgetBase(ctx, db, base)
	} else {
		n, err = forgetSource(ctx, db, base, source)
	}
	if err != nil {
		if isMissingTable(err) {
			return forgottenResult(job, 0), nil
		}
		return params.Err(job, "db", err.Error()), nil
	}
	return forgottenResult(job, n), nil
}

func forgottenResult(job core.Job, n int) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"forgotten": {MIME: "application/json", Inline: n}},
	}
}
