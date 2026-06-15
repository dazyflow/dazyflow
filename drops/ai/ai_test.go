package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

// textServer replies with one text block, capturing the request body.
func textServer(t *testing.T, reply string, gotBody *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, gotBody)
		if r.Header.Get("x-api-key") != "sk-ant-test" || r.Header.Get("anthropic-version") != apiVersion {
			t.Errorf("bad headers: %v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []any{map[string]any{"type": "text", "text": reply}},
		})
	}))
}

// toolServer replies with one tool_use block carrying input.
func toolServer(t *testing.T, name string, input map[string]any, gotBody *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []any{map[string]any{"type": "tool_use", "name": name, "input": input}},
		})
	}))
}

func TestSummarize_TextInputAndOutput(t *testing.T) {
	var body map[string]any
	srv := textServer(t, "A short summary.", &body)
	defer srv.Close()

	res, err := executeSummarize(context.Background(), core.Job{
		Params: map[string]any{"api_key": "sk-ant-test", "base_url": srv.URL, "style": "one_line", "max_words": 20},
		Input:  map[string]core.Ref{"text": {Inline: "a very long piece of text"}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if res.Output["summary"].Inline != "A short summary." {
		t.Errorf("summary = %v", res.Output["summary"].Inline)
	}
	// The text rode in on the input port → single user message.
	msgs := body["messages"].([]any)
	if msgs[0].(map[string]any)["content"] != "a very long piece of text" {
		t.Errorf("messages = %+v", msgs)
	}
	if _, hasTool := body["tools"]; hasTool {
		t.Error("summarize must not send a tool")
	}
}

func TestExtract_ForcesToolAndReturnsData(t *testing.T) {
	var body map[string]any
	srv := toolServer(t, "extract", map[string]any{"amount": 42.0, "vendor": "Acme"}, &body)
	defer srv.Close()

	res, err := executeExtract(context.Background(), core.Job{
		Params: map[string]any{
			"api_key": "sk-ant-test", "base_url": srv.URL,
			"text": "Invoice from Acme for $42",
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
	// Forced tool_choice on the extract tool.
	tc := body["tool_choice"].(map[string]any)
	if tc["type"] != "tool" || tc["name"] != "extract" {
		t.Errorf("tool_choice = %+v", tc)
	}
}

func TestExtract_NoFields(t *testing.T) {
	res, _ := executeExtract(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "text": "hi"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestClassify_ReturnsCategoryAndConfidence(t *testing.T) {
	var body map[string]any
	srv := toolServer(t, "classify", map[string]any{"category": "billing", "confidence": 0.9}, &body)
	defer srv.Close()

	res, err := executeClassify(context.Background(), core.Job{
		Params: map[string]any{
			"api_key": "sk-ant-test", "base_url": srv.URL,
			"text": "I was double charged",
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
	// The enum constrains the model's choice to the declared names.
	tool := body["tools"].([]any)[0].(map[string]any)
	schema := tool["input_schema"].(map[string]any)
	enum := schema["properties"].(map[string]any)["category"].(map[string]any)["enum"].([]any)
	if len(enum) != 2 {
		t.Errorf("enum = %+v (allow_none default false → no 'none')", enum)
	}
}

func TestClassify_TooFewCategories(t *testing.T) {
	res, _ := executeClassify(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k", "text": "hi", "categories": []any{map[string]any{"name": "only"}}},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestDraftReply_AppendsSignature(t *testing.T) {
	var body map[string]any
	srv := textServer(t, "Thanks for reaching out!", &body)
	defer srv.Close()

	res, err := executeDraftReply(context.Background(), core.Job{
		Params: map[string]any{
			"api_key": "sk-ant-test", "base_url": srv.URL,
			"text": "Hi, I need help", "tone": "friendly", "signature": "— Support",
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if res.Output["reply"].Inline != "Thanks for reaching out!\n\n— Support" {
		t.Errorf("reply = %q", res.Output["reply"].Inline)
	}
}

func TestMissingAPIKey(t *testing.T) {
	res, _ := executeSummarize(context.Background(), core.Job{
		Params: map[string]any{"text": "hi"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestNoText(t *testing.T) {
	res, _ := executeSummarize(context.Background(), core.Job{
		Params: map[string]any{"api_key": "k"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
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
