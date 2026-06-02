package notify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
	hfnet "git.sr.ht/~klahr/hazyflow/drops/net"
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
		Input:  map[string]core.Ref{"body": {Inline: "deploy done"}},
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
