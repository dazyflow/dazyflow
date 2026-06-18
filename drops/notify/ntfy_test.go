package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

func TestNtfy_PostsBodyAndHeaders(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	var gotPath, gotTitle, gotTags string
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotTitle = r.Header.Get("Title")
		gotTags = r.Header.Get("Tags")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()

	res, err := executeNtfy(context.Background(), core.Job{
		Params: map[string]any{"server": srv.URL, "topic": "alerts", "title": "Hi", "tags": []any{"tada", "rocket"}},
		Input:  map[string]core.Ref{"message": {Inline: "deploy done"}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if gotPath != "/alerts" || gotTitle != "Hi" || gotBody != "deploy done" {
		t.Errorf("path=%q title=%q body=%q", gotPath, gotTitle, gotBody)
	}
	if gotTags != "tada,rocket" {
		t.Errorf("tags = %q", gotTags)
	}
}

// A body over ntfy's message limit would be treated as a file-attachment
// upload (rejected by public servers) — the drop truncates instead, without
// splitting a multi-byte character.
func TestNtfy_TruncatesLongMessage(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)

	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"x"}`))
	}))
	defer srv.Close()

	// 5000 bytes of "å" (2 bytes each) — over the 4000-byte cap, with the cut
	// landing mid-rune unless the truncation backs up to a rune boundary.
	long := strings.Repeat("å", 2500)
	// Buffered so the non-blocking emitProgress always lands; assert we warned.
	prog := make(chan core.Progress, 4)
	res, err := executeNtfy(context.Background(), core.Job{
		Params: map[string]any{"server": srv.URL, "topic": "alerts"},
		Input:  map[string]core.Ref{"message": {Inline: long}},
	}, prog)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if len(gotBody) > 4003 { // 4000 + ellipsis (3 bytes)
		t.Errorf("body not truncated: %d bytes", len(gotBody))
	}
	if !strings.HasSuffix(gotBody, "…") {
		t.Errorf("truncated body should end with ellipsis")
	}
	if !strings.HasPrefix(gotBody, "å") || strings.ContainsRune(gotBody, '�') {
		t.Errorf("multi-byte characters must not be split")
	}
	// The truncation must be surfaced, not silent: a progress warning…
	close(prog)
	warned := false
	for p := range prog {
		if strings.Contains(p.Message, "shortened") {
			warned = true
		}
	}
	if !warned {
		t.Error("expected a progress warning about the shortened message")
	}
	// …and a flag in the result meta.
	meta, _ := res.Output["meta"].Inline.(map[string]any)
	if meta["truncated"] != true {
		t.Errorf("meta.truncated = %v, want true", meta["truncated"])
	}
	if meta["original_bytes"] != 5000 {
		t.Errorf("meta.original_bytes = %v, want 5000", meta["original_bytes"])
	}
}

func TestNtfy_MissingTopic(t *testing.T) {
	res, _ := executeNtfy(context.Background(), core.Job{Params: map[string]any{"message": "hi"}}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%v", res.Status, res.Error)
	}
}

func TestNtfy_ServerError(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	defer hfnet.SetAllowPrivateEgress(false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte("forbidden"))
	}))
	defer srv.Close()
	res, _ := executeNtfy(context.Background(), core.Job{
		Params: map[string]any{"server": srv.URL, "topic": "t", "message": "m"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "ntfy_error" || !strings.Contains(res.Error.Message, "403") {
		t.Errorf("err = %+v", res.Error)
	}
}
