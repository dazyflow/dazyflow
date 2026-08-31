// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package llmtask

import (
	"context"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// fakeProvider records the Request and returns a canned Result.
type fakeProvider struct {
	text   string
	tool   map[string]any
	gotReq Request
}

func (f *fakeProvider) Call(_ context.Context, _ string, req Request) (Result, *core.JobError) {
	f.gotReq = req
	return Result{Text: f.text, Tool: f.tool, Raw: map[string]any{"ok": true}}, nil
}

func testCfg(p Provider) Config {
	return Config{
		Provider: p, Integration: "Test", DefaultModel: "m1",
		Models: []ModelOption{{ID: "m1", Label: "M1"}},
		AskID:  "test_ask", TaskIDPrefix: "test",
	}
}

func TestSummarize_MapsTextAndModel(t *testing.T) {
	fp := &fakeProvider{text: "A short summary."}
	res, err := summarizeDrop(testCfg(fp)).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "style": "one_line"},
		Input:  map[string]core.Ref{"text": {Inline: "a long piece of text"}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if res.Output["summary"].Inline != "A short summary." {
		t.Errorf("summary = %v", res.Output["summary"].Inline)
	}
	if fp.gotReq.UserText != "a long piece of text" {
		t.Errorf("userText = %q", fp.gotReq.UserText)
	}
	if fp.gotReq.Model != "m1" {
		t.Errorf("model = %q (want default)", fp.gotReq.Model)
	}
	if fp.gotReq.Tool != nil {
		t.Error("summarize must not force a tool")
	}
}

func TestMissingAPIKey(t *testing.T) {
	res, _ := summarizeDrop(testCfg(&fakeProvider{})).Execute(context.Background(), core.Job{
		Params: map[string]any{}, Input: map[string]core.Ref{"text": {Inline: "hi"}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestSummarize_NoText(t *testing.T) {
	res, _ := summarizeDrop(testCfg(&fakeProvider{})).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestExtract_ForcesToolAndReturnsData(t *testing.T) {
	fp := &fakeProvider{tool: map[string]any{"amount": 42.0, "vendor": "Acme"}}
	res, err := extractDrop(testCfg(fp)).Execute(context.Background(), core.Job{
		Params: map[string]any{
			"api_key": "k", "text": "Invoice from Acme for $42",
			"fields": []any{
				map[string]any{"name": "amount", "type": "number"},
				map[string]any{"name": "vendor"},
			},
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	data := res.Output["data"].Inline.(map[string]any)
	if data["vendor"] != "Acme" || data["amount"] != 42.0 {
		t.Errorf("data = %+v", data)
	}
	if fp.gotReq.Tool == nil || fp.gotReq.Tool.Name != "extract" {
		t.Errorf("tool not forced: %+v", fp.gotReq.Tool)
	}
}

func TestExtract_NoFields(t *testing.T) {
	res, _ := extractDrop(testCfg(&fakeProvider{})).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "text": "hi"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestClassify_ReturnsCategoryAndConfidence(t *testing.T) {
	fp := &fakeProvider{tool: map[string]any{"category": "billing", "confidence": 0.9}}
	res, err := classifyDrop(testCfg(fp)).Execute(context.Background(), core.Job{
		Params: map[string]any{
			"api_key": "k", "text": "I was double charged",
			"categories": []any{
				map[string]any{"name": "billing", "description": "payments"},
				map[string]any{"name": "technical", "description": "bugs"},
			},
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if res.Output["category"].Inline != "billing" {
		t.Errorf("category = %v", res.Output["category"].Inline)
	}
	if res.Output["confidence"].Inline != 0.9 {
		t.Errorf("confidence = %v", res.Output["confidence"].Inline)
	}
	// allow_none defaults false → enum has exactly the two declared names.
	enum := fp.gotReq.Tool.Schema["properties"].(map[string]any)["category"].(map[string]any)["enum"].([]string)
	if len(enum) != 2 {
		t.Errorf("enum = %+v", enum)
	}
}

func TestDraftReply_AppendsSignature(t *testing.T) {
	fp := &fakeProvider{text: "Thanks for reaching out!"}
	res, err := draftReplyDrop(testCfg(fp)).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "text": "Hi, I need help", "signature": "— Support"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if res.Output["reply"].Inline != "Thanks for reaching out!\n\n— Support" {
		t.Errorf("reply = %q", res.Output["reply"].Inline)
	}
}

func TestAsk_PromptInput(t *testing.T) {
	fp := &fakeProvider{text: "Hello!"}
	res, err := askDrop(testCfg(fp)).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k"},
		Input:  map[string]core.Ref{"prompt": {Inline: "Say hi"}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if res.Output["text"].Inline != "Hello!" || fp.gotReq.UserText != "Say hi" {
		t.Errorf("text=%v userText=%q", res.Output["text"].Inline, fp.gotReq.UserText)
	}
}

func TestCoerceText(t *testing.T) {
	if coerceText([]any{"a", "b"}) != "a\n\nb" {
		t.Error("list join failed")
	}
	if coerceText(map[string]any{"value": "wrapped"}) != "wrapped" {
		t.Error("value-wrapper failed")
	}
}

func TestHTTPError_ActionableByStatus(t *testing.T) {
	cases := []struct {
		status   int
		wantCode string
		wantSub  string // a phrase the user-facing message must contain
	}{
		{401, "openai_auth", "API key"},
		{403, "openai_auth", "API key"},
		{429, "openai_rate_limited", "rate-limited"},
		{400, "openai_bad_request", "model name"},
		{404, "openai_not_found", "model"},
		{503, "openai_upstream", "try again"},
		{418, "openai_api", "HTTP 418"},
	}
	for _, c := range cases {
		je := HTTPError("openai", "ChatGPT", c.status, "quota exceeded")
		if je.Code != c.wantCode {
			t.Errorf("status %d: code=%q, want %q", c.status, je.Code, c.wantCode)
		}
		if !strings.Contains(je.Message, "ChatGPT") {
			t.Errorf("status %d: message %q missing integration label", c.status, je.Message)
		}
		if !strings.Contains(je.Message, c.wantSub) {
			t.Errorf("status %d: message %q missing %q", c.status, je.Message, c.wantSub)
		}
		// The vendor detail is appended for debugging.
		if !strings.Contains(je.Message, "quota exceeded") {
			t.Errorf("status %d: message %q dropped vendor detail", c.status, je.Message)
		}
	}
	// 429 keeps the legacy stable code the UI may key on.
	if HTTPError("claude", "Claude", 429, "").Code != "claude_rate_limited" {
		t.Error("claude 429 code regressed")
	}
}

// errProvider returns a fixed JobError from Call, exercising the post-call
// error path every drop shares. recordProvider captures the Request like the
// existing fakeProvider but also lets a test force an empty Result.
type errProvider struct{ je *core.JobError }

func (e *errProvider) Call(_ context.Context, _ string, _ Request) (Result, *core.JobError) {
	return Result{}, e.je
}

// nilToolProvider returns a Result with no Tool (text-only), to hit the
// "model did not return …" branches in extract and classify.
type nilToolProvider struct{ text string }

func (p *nilToolProvider) Call(_ context.Context, _ string, _ Request) (Result, *core.JobError) {
	return Result{Text: p.text}, nil
}

func TestCoerceText_Cov(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"string", "hi", "hi"},
		{"bytes", []byte("raw"), "raw"},
		{"list joins non-empty", []any{"a", "", "b"}, "a\n\nb"},
		{"value wrapper", map[string]any{"value": "wrapped"}, "wrapped"},
		{"map without value marshals", map[string]any{"k": "v"}, `{"k":"v"}`},
		{"scalar marshals", 42, "42"},
		{"nested value wrapper", map[string]any{"value": []any{"x", "y"}}, "x\n\ny"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := coerceText(c.in); got != c.want {
				t.Fatalf("coerceText(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestParamObjList(t *testing.T) {
	t.Run("missing key", func(t *testing.T) {
		if got := paramObjList(map[string]any{}, "k"); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})
	t.Run("wrong type", func(t *testing.T) {
		if got := paramObjList(map[string]any{"k": "nope"}, "k"); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})
	t.Run("non-map items skipped", func(t *testing.T) {
		got := paramObjList(map[string]any{"k": []any{
			map[string]any{"name": "a"}, "skip", 7, map[string]any{"name": "b"},
		}}, "k")
		if len(got) != 2 || got[0]["name"] != "a" || got[1]["name"] != "b" {
			t.Fatalf("got %+v, want two maps", got)
		}
	})
}

func TestSummarizeSystem(t *testing.T) {
	cases := []struct {
		name     string
		style    string
		maxWords int
		lang     string
		wantSub  []string
		notSub   []string
	}{
		{"one_line", "one_line", 60, "", []string{"single sentence", "under about 60 words"}, nil},
		{"bullets", "bullets", 0, "", []string{"bullet points"}, []string{"under about"}},
		{"paragraph default", "paragraph", 30, "French", []string{"single short paragraph", "in French"}, nil},
		{"unknown style falls to paragraph", "weird", 0, "", []string{"single short paragraph"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := summarizeSystem(c.style, c.maxWords, c.lang)
			for _, sub := range c.wantSub {
				if !strings.Contains(s, sub) {
					t.Errorf("missing %q in %q", sub, s)
				}
			}
			for _, sub := range c.notSub {
				if strings.Contains(s, sub) {
					t.Errorf("unexpected %q in %q", sub, s)
				}
			}
		})
	}
}

func TestDraftReplySystem(t *testing.T) {
	cases := []struct {
		name    string
		tone    string
		guide   string
		lang    string
		wantSub []string
	}{
		{"formal", "formal", "", "", []string{"formal, professional"}},
		{"concise", "concise", "", "", []string{"brief, to-the-point"}},
		{"apologetic", "apologetic", "", "", []string{"apologetic"}},
		{"friendly default", "friendly", "", "", []string{"friendly, helpful"}},
		{"unknown tone falls to friendly", "weird", "", "", []string{"friendly, helpful"}},
		{"guidance and language", "formal", "offer a refund", "German", []string{"offer a refund", "in German"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := draftReplySystem(c.tone, c.guide, c.lang)
			for _, sub := range c.wantSub {
				if !strings.Contains(s, sub) {
					t.Errorf("missing %q in %q", sub, s)
				}
			}
		})
	}
}

func TestCategoryNames(t *testing.T) {
	names, lines := categoryNames([]map[string]any{
		{"name": "billing", "description": "payments"},
		{"name": " "},              // blank name skipped
		{"name": "tech"},           // no description
		{"description": "no name"}, // missing name skipped
	})
	if len(names) != 2 || names[0] != "billing" || names[1] != "tech" {
		t.Fatalf("names = %v", names)
	}
	if !strings.Contains(lines, "- billing: payments") {
		t.Errorf("desc line missing: %q", lines)
	}
	if !strings.Contains(lines, "- tech\n") {
		t.Errorf("bare name line missing: %q", lines)
	}
}

func TestBuildExtractTool(t *testing.T) {
	t.Run("no usable fields returns nil", func(t *testing.T) {
		if tool := buildExtractTool([]map[string]any{{"name": " "}, {}}, "null"); tool != nil {
			t.Fatalf("want nil, got %+v", tool)
		}
	})

	t.Run("types map and date augments description", func(t *testing.T) {
		tool := buildExtractTool([]map[string]any{
			{"name": "amount", "type": "number"},
			{"name": "active", "type": "boolean"},
			{"name": "due", "type": "date"},
			{"name": "note", "description": "free text"},
		}, "null")
		if tool == nil || tool.Name != "extract" {
			t.Fatalf("tool = %+v", tool)
		}
		props := tool.Schema["properties"].(map[string]any)
		// on_missing=null → types are nullable [type, "null"].
		amt := props["amount"].(map[string]any)["type"].([]any)
		if amt[0] != "number" || amt[1] != "null" {
			t.Errorf("amount type = %v", amt)
		}
		due := props["due"].(map[string]any)
		if !strings.Contains(due["description"].(string), "ISO 8601") {
			t.Errorf("date description = %v", due["description"])
		}
		note := props["note"].(map[string]any)
		if note["description"] != "free text" {
			t.Errorf("note description = %v", note["description"])
		}
		if _, hasReq := tool.Schema["required"]; hasReq {
			t.Error("on_missing=null must not set required")
		}
	})

	t.Run("on_missing=fail sets required and non-nullable types", func(t *testing.T) {
		tool := buildExtractTool([]map[string]any{{"name": "amount", "type": "number"}}, "fail")
		props := tool.Schema["properties"].(map[string]any)
		if props["amount"].(map[string]any)["type"] != "number" {
			t.Errorf("type = %v, want plain number", props["amount"])
		}
		req := tool.Schema["required"].([]string)
		if len(req) != 1 || req[0] != "amount" {
			t.Errorf("required = %v", req)
		}
	})
}

// --- drop-level error and branch paths -------------------------------------

func TestSummarize_ProviderError(t *testing.T) {
	fp := &errProvider{je: &core.JobError{Code: "openai_rate_limited", Message: "slow down"}}
	res, _ := summarizeDrop(testCfg(fp)).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k"},
		Input:  map[string]core.Ref{"text": {Inline: "long text"}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "openai_rate_limited" {
		t.Fatalf("res = %+v", res)
	}
}

func TestSummarize_StyleAndLanguageReachSystem(t *testing.T) {
	fp := &fakeProvider{text: "ok"}
	_, _ = summarizeDrop(testCfg(fp)).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "style": "bullets", "max_words": 20, "language": "Spanish", "text": "x"},
	}, nil)
	if !strings.Contains(fp.gotReq.System, "bullet points") || !strings.Contains(fp.gotReq.System, "in Spanish") {
		t.Fatalf("system = %q", fp.gotReq.System)
	}
}

func TestExtract_ProviderError(t *testing.T) {
	fp := &errProvider{je: &core.JobError{Code: "openai_auth", Message: "bad key"}}
	res, _ := extractDrop(testCfg(fp)).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "text": "hi", "fields": []any{map[string]any{"name": "x"}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "openai_auth" {
		t.Fatalf("res = %+v", res)
	}
}

func TestExtract_NoText(t *testing.T) {
	res, _ := extractDrop(testCfg(&fakeProvider{})).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "fields": []any{map[string]any{"name": "x"}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Fatalf("res = %+v", res)
	}
}

func TestExtract_NoToolOutput(t *testing.T) {
	res, _ := extractDrop(testCfg(&nilToolProvider{})).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "text": "hi", "fields": []any{map[string]any{"name": "x"}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "ai_no_output" {
		t.Fatalf("res = %+v", res)
	}
}

func TestClassify_NoText(t *testing.T) {
	res, _ := classifyDrop(testCfg(&fakeProvider{})).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "categories": []any{
			map[string]any{"name": "a"}, map[string]any{"name": "b"},
		}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Fatalf("res = %+v", res)
	}
}

func TestClassify_TooFewCategories(t *testing.T) {
	res, _ := classifyDrop(testCfg(&fakeProvider{})).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "text": "hi", "categories": []any{map[string]any{"name": "only"}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("res = %+v", res)
	}
}

func TestClassify_AllowNoneAddsNoneAndSystemHint(t *testing.T) {
	fp := &fakeProvider{tool: map[string]any{"category": "none"}}
	res, _ := classifyDrop(testCfg(fp)).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "text": "hi", "allow_none": true, "categories": []any{
			map[string]any{"name": "a"}, map[string]any{"name": "b"},
		}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	enum := fp.gotReq.Tool.Schema["properties"].(map[string]any)["category"].(map[string]any)["enum"].([]string)
	if len(enum) != 3 || enum[2] != "none" {
		t.Errorf("enum = %v, want [a b none]", enum)
	}
	if !strings.Contains(fp.gotReq.System, `"none"`) {
		t.Errorf("system missing none hint: %q", fp.gotReq.System)
	}
}

func TestClassify_ProviderError(t *testing.T) {
	fp := &errProvider{je: &core.JobError{Code: "openai_upstream", Message: "down"}}
	res, _ := classifyDrop(testCfg(fp)).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "text": "hi", "categories": []any{
			map[string]any{"name": "a"}, map[string]any{"name": "b"},
		}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "openai_upstream" {
		t.Fatalf("res = %+v", res)
	}
}

func TestClassify_NoToolOutput(t *testing.T) {
	res, _ := classifyDrop(testCfg(&nilToolProvider{})).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "text": "hi", "categories": []any{
			map[string]any{"name": "a"}, map[string]any{"name": "b"},
		}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "ai_no_output" {
		t.Fatalf("res = %+v", res)
	}
}

func TestClassify_EmptyCategory(t *testing.T) {
	fp := &fakeProvider{tool: map[string]any{"category": ""}}
	res, _ := classifyDrop(testCfg(fp)).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "text": "hi", "categories": []any{
			map[string]any{"name": "a"}, map[string]any{"name": "b"},
		}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "ai_no_output" {
		t.Fatalf("res = %+v", res)
	}
}

func TestDraftReply_NoMessage(t *testing.T) {
	res, _ := draftReplyDrop(testCfg(&fakeProvider{})).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Fatalf("res = %+v", res)
	}
}

func TestDraftReply_ProviderError(t *testing.T) {
	fp := &errProvider{je: &core.JobError{Code: "openai_bad_request", Message: "nope"}}
	res, _ := draftReplyDrop(testCfg(fp)).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "text": "hi"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "openai_bad_request" {
		t.Fatalf("res = %+v", res)
	}
}

func TestDraftReply_NoSignatureLeavesReply(t *testing.T) {
	fp := &fakeProvider{text: "  Hello there  "}
	res, _ := draftReplyDrop(testCfg(fp)).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "text": "hi"},
	}, nil)
	if res.Output["reply"].Inline != "Hello there" {
		t.Fatalf("reply = %q (want trimmed, no signature)", res.Output["reply"].Inline)
	}
}

func TestAsk_ProviderError(t *testing.T) {
	fp := &errProvider{je: &core.JobError{Code: "openai_auth", Message: "bad"}}
	res, _ := askDrop(testCfg(fp)).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "prompt": "hi"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "openai_auth" {
		t.Fatalf("res = %+v", res)
	}
}

func TestAsk_NoPromptOrMessages(t *testing.T) {
	res, _ := askDrop(testCfg(&fakeProvider{})).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Fatalf("res = %+v", res)
	}
}

func TestAsk_MessagesParamUsed(t *testing.T) {
	fp := &fakeProvider{text: "ok"}
	msgs := []any{map[string]any{"role": "user", "content": "hi"}}
	res, _ := askDrop(testCfg(fp)).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "messages": msgs},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	if len(fp.gotReq.Messages) != 1 {
		t.Fatalf("messages not forwarded: %+v", fp.gotReq.Messages)
	}
}

func TestAsk_ParamPromptFallback(t *testing.T) {
	fp := &fakeProvider{text: "ok"}
	_, _ = askDrop(testCfg(fp)).Execute(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "prompt": "from param", "temperature": 0.7, "max_tokens": 256},
	}, nil)
	if fp.gotReq.UserText != "from param" {
		t.Errorf("userText = %q", fp.gotReq.UserText)
	}
	if fp.gotReq.Temperature == nil || *fp.gotReq.Temperature != 0.7 {
		t.Errorf("temperature = %v", fp.gotReq.Temperature)
	}
	if fp.gotReq.MaxTokens != 256 {
		t.Errorf("maxTokens = %d", fp.gotReq.MaxTokens)
	}
}

func TestResolveText_ParamFallbackWhenInputEmpty(t *testing.T) {
	// Input present but coerces to empty → falls through to the text param.
	job := core.Job{
		Params: map[string]any{"text": "param text"},
		Input:  map[string]core.Ref{"text": {Inline: ""}},
	}
	if got := resolveText(job); got != "param text" {
		t.Fatalf("resolveText = %q", got)
	}
}
