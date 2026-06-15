package llmtask

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
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
