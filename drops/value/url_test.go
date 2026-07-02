// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package value

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func TestURL_EmitsAndParsesQuery(t *testing.T) {
	res, err := executeURL(t.Context(), core.Job{
		Params: map[string]any{"url": "https://example.com/search?q=hello&page=2"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if out := res.Output["out"]; out.MIME != "text/plain" || out.Inline != "https://example.com/search?q=hello&page=2" {
		t.Errorf("out = %+v", out)
	}
	q, ok := res.Output["query"].Inline.(map[string]any)
	if !ok {
		t.Fatalf("query Inline is %T, want map", res.Output["query"].Inline)
	}
	if q["q"] != "hello" || q["page"] != "2" {
		t.Errorf("query map = %+v, want {q:hello page:2}", q)
	}
}

func TestURL_EmitsHostAndPath(t *testing.T) {
	res, _ := executeURL(t.Context(), core.Job{
		Params: map[string]any{"url": "https://example.com/blog/post?x=1"},
	}, nil)
	if h := res.Output["host"]; h.MIME != "text/plain" || h.Inline != "example.com" {
		t.Errorf("host = %+v, want example.com", h)
	}
	if p := res.Output["path"]; p.MIME != "text/plain" || p.Inline != "/blog/post" {
		t.Errorf("path = %+v, want /blog/post", p)
	}
}

func TestURL_HostKeepsPort_PathEmptyForBareHost(t *testing.T) {
	// u.Host keeps an explicit port; a bare host with no path yields "".
	res, _ := executeURL(t.Context(), core.Job{
		Params: map[string]any{"url": "http://localhost:8080"},
	}, nil)
	if h := res.Output["host"].Inline; h != "localhost:8080" {
		t.Errorf("host = %v, want localhost:8080", h)
	}
	if p := res.Output["path"].Inline; p != "" {
		t.Errorf("path = %q, want empty for a bare host", p)
	}
}

func TestURL_WiredInputWinsOverParam(t *testing.T) {
	res, _ := executeURL(t.Context(), core.Job{
		Params: map[string]any{"url": "https://from-param.example"},
		Input:  map[string]core.Ref{"url": {Inline: "https://from-wire.example/x?a=1"}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if res.Output["out"].Inline != "https://from-wire.example/x?a=1" {
		t.Errorf("wired input should win, got %v", res.Output["out"].Inline)
	}
}

func TestURL_DecodesQueryValues(t *testing.T) {
	res, _ := executeURL(t.Context(), core.Job{
		Params: map[string]any{"url": "https://example.com/?msg=hello%20world&tag=a&tag=b"},
	}, nil)
	q := res.Output["query"].Inline.(map[string]any)
	if q["msg"] != "hello world" {
		t.Errorf("value not percent-decoded: %q", q["msg"])
	}
	if q["tag"] != "a" { // first value wins on a repeated key
		t.Errorf("repeated key: got %q, want first value \"a\"", q["tag"])
	}
}

func TestURL_TrimsWhitespace(t *testing.T) {
	res, _ := executeURL(t.Context(), core.Job{
		Params: map[string]any{"url": "  https://example.com/x\n"},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if res.Output["out"].Inline != "https://example.com/x" {
		t.Errorf("expected trimmed URL, got %q", res.Output["out"].Inline)
	}
}

func TestURL_RejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"no scheme":     "example.com/path",
		"non-http":      "ftp://example.com/file",
		"scheme only":   "https://",
		"just a word":   "hello",
		"relative path": "/foo/bar?x=1",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := executeURL(t.Context(), core.Job{
				Params: map[string]any{"url": in},
			}, nil)
			if err != nil {
				t.Fatalf("execute returned a transport error: %v", err)
			}
			if res.Status == core.StatusOK {
				t.Errorf("expected failure for %q, got OK with out=%v", in, res.Output["out"].Inline)
			}
		})
	}
}
