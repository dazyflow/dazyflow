// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package rss

import (
	"context"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

const rssSample = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/">
  <channel>
    <title>My Blog</title>
    <item>
      <title>Post One</title>
      <link>https://example.com/1</link>
      <guid>guid-1</guid>
      <pubDate>Mon, 02 Jan 2006 15:04:05 -0700</pubDate>
      <description>First post</description>
      <dc:creator>Ada</dc:creator>
    </item>
    <item>
      <title>Post Two</title>
      <link>https://example.com/2</link>
      <guid>guid-2</guid>
      <description>Second post</description>
    </item>
  </channel>
</rss>`

const atomSample = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>My Feed</title>
  <entry>
    <title>Atom One</title>
    <id>urn:uuid:1</id>
    <link href="https://example.com/edit" rel="edit"/>
    <link href="https://example.com/a1" rel="alternate"/>
    <published>2026-07-02T13:00:00Z</published>
    <summary>An atom entry</summary>
    <author><name>Grace</name></author>
  </entry>
</feed>`

func TestParseFeed_RSS(t *testing.T) {
	items, err := parseFeed([]byte(rssSample))
	if err != nil {
		t.Fatalf("parseFeed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	it := items[0]
	if it.ID != "guid-1" || it.Title != "Post One" || it.Link != "https://example.com/1" {
		t.Errorf("item0 = %+v", it)
	}
	if it.Summary != "First post" {
		t.Errorf("summary = %q", it.Summary)
	}
	if it.Author != "Ada" { // dc:creator fallback
		t.Errorf("author = %q, want Ada", it.Author)
	}
	// pubDate normalized to RFC3339 UTC (15:04:05 -0700 → 22:04:05Z).
	if it.Published != "2006-01-02T22:04:05Z" {
		t.Errorf("published = %q, want 2006-01-02T22:04:05Z", it.Published)
	}
}

func TestParseFeed_Atom(t *testing.T) {
	items, err := parseFeed([]byte(atomSample))
	if err != nil {
		t.Fatalf("parseFeed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	it := items[0]
	if it.ID != "urn:uuid:1" || it.Title != "Atom One" {
		t.Errorf("item = %+v", it)
	}
	if it.Link != "https://example.com/a1" { // rel=alternate wins over rel=edit
		t.Errorf("link = %q, want the alternate link", it.Link)
	}
	if it.Published != "2026-07-02T13:00:00Z" {
		t.Errorf("published = %q", it.Published)
	}
	if it.Author != "Grace" {
		t.Errorf("author = %q, want Grace", it.Author)
	}
}

func TestParseFeed_EmptyChannelOK(t *testing.T) {
	items, err := parseFeed([]byte(`<rss version="2.0"><channel><title>Empty</title></channel></rss>`))
	if err != nil {
		t.Fatalf("empty channel should not error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("got %d items, want 0", len(items))
	}
}

func TestParseFeed_NotAFeed(t *testing.T) {
	if _, err := parseFeed([]byte(`<html><body>hi</body></html>`)); err == nil {
		t.Error("non-feed HTML should error")
	}
	if _, err := parseFeed([]byte(`not xml at all`)); err == nil {
		t.Error("garbage should error")
	}
}

// memStore installs an in-memory cursor store for the duration of a test.
func memStore(t *testing.T) map[string]string {
	t.Helper()
	store := map[string]string{}
	SetCursorStore(
		func(_ context.Context, tenant, name string) (string, error) { return store[tenant+"/"+name], nil },
		func(_ context.Context, tenant, name, value string) error { store[tenant+"/"+name] = value; return nil },
	)
	t.Cleanup(func() { SetCursorStore(nil, nil) })
	return store
}

func item(id string) feedItem { return feedItem{ID: id, Title: id} }

func freshIDs(res core.Result) []string {
	rows, _ := res.Output["items"].Inline.([]map[string]any)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r["id"].(string))
	}
	return out
}

func TestDedupe_FirstRunBaselinesSilently(t *testing.T) {
	memStore(t)
	job := core.Job{ID: "j", GraphID: "g", NodeID: "n", Tenant: "t"}
	res := dedupeAndEmit(context.Background(), job, []feedItem{item("A"), item("B")}, nil)
	if len(res.Output) != 0 {
		t.Errorf("first run should emit nothing, got %d ports", len(res.Output))
	}
}

func TestDedupe_EmitsOnlyNewOnSecondRun(t *testing.T) {
	memStore(t)
	job := core.Job{ID: "j", GraphID: "g", NodeID: "n", Tenant: "t"}
	// First run baselines on A, B.
	dedupeAndEmit(context.Background(), job, []feedItem{item("A"), item("B")}, nil)
	// Second run: C is new (feed newest-first), A/B already seen.
	res := dedupeAndEmit(context.Background(), job, []feedItem{item("C"), item("A"), item("B")}, nil)
	got := freshIDs(res)
	if len(got) != 1 || got[0] != "C" {
		t.Errorf("fresh = %v, want [C]", got)
	}
}

func TestDedupe_NothingNewEmitsNothing(t *testing.T) {
	memStore(t)
	job := core.Job{ID: "j", GraphID: "g", NodeID: "n", Tenant: "t"}
	dedupeAndEmit(context.Background(), job, []feedItem{item("A"), item("B")}, nil)
	res := dedupeAndEmit(context.Background(), job, []feedItem{item("A"), item("B")}, nil)
	if len(res.Output) != 0 {
		t.Errorf("no-new run should emit nothing, got %d ports", len(res.Output))
	}
}

func TestDedupe_IndependentPerNode(t *testing.T) {
	memStore(t)
	base := core.Job{ID: "j", GraphID: "g", Tenant: "t"}
	n1 := base
	n1.NodeID = "n1"
	n2 := base
	n2.NodeID = "n2"
	// n1 baselines on A; n2 has never seen anything, so its first run also
	// baselines (independent cursor) — proving keys don't collide.
	dedupeAndEmit(context.Background(), n1, []feedItem{item("A")}, nil)
	res := dedupeAndEmit(context.Background(), n2, []feedItem{item("A")}, nil)
	if len(res.Output) != 0 {
		t.Errorf("n2 first run should baseline silently, got %d ports", len(res.Output))
	}
}

// drainProgress runs fn with a buffered progress channel and returns every
// message emitted — the run-log lines that explain an empty Items output.
func drainProgress(job core.Job, items []feedItem) []string {
	ch := make(chan core.Progress, 8)
	dedupeAndEmit(context.Background(), job, items, ch)
	close(ch)
	var msgs []string
	for p := range ch {
		msgs = append(msgs, p.Message)
	}
	return msgs
}

func TestDedupe_LogsExplainEmptyOutput(t *testing.T) {
	memStore(t)
	job := core.Job{ID: "j", GraphID: "g", NodeID: "n", Tenant: "t"}

	// First run: baseline line names the count and says it's watching.
	if msgs := drainProgress(job, []feedItem{item("A"), item("B")}); len(msgs) != 1 ||
		!strings.Contains(msgs[0], "baseline") || !strings.Contains(msgs[0], "2") {
		t.Errorf("baseline log = %v, want a 'baseline'/count line", msgs)
	}
	// Nothing new: an explicit "no new items" line (not silence).
	if msgs := drainProgress(job, []feedItem{item("A"), item("B")}); len(msgs) != 1 ||
		!strings.Contains(msgs[0], "no new items") {
		t.Errorf("no-new log = %v, want a 'no new items' line", msgs)
	}
	// New item: a "1 new item(s)" line accompanies the emitted row.
	if msgs := drainProgress(job, []feedItem{item("C"), item("A"), item("B")}); len(msgs) != 1 ||
		!strings.Contains(msgs[0], "1 new item") {
		t.Errorf("new-item log = %v, want a '1 new item' line", msgs)
	}
}

func TestCapIDs(t *testing.T) {
	ids := make([]string, 600)
	for i := range ids {
		ids[i] = string(rune('a' + i%26))
	}
	if got := capIDs(ids, maxSeenIDs); len(got) != maxSeenIDs {
		t.Errorf("capIDs len = %d, want %d", len(got), maxSeenIDs)
	}
	if got := capIDs([]string{"x"}, maxSeenIDs); len(got) != 1 {
		t.Errorf("short slice should be unchanged, got %d", len(got))
	}
}

func TestDedupeIDs(t *testing.T) {
	got := dedupeIDs([]string{"a", "b", "a", "", "c", "b"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
			break
		}
	}
}

// The reset registry must resolve to exactly the cursor key dedupeAndEmit
// uses — the contract the daemon's "Reset state" endpoint relies on. If the
// key format ever drifts from cursorName, a reset would delete the wrong (or
// no) key, silently leaving the node's memory intact.
func TestStateResetKeys_MatchesCursorName(t *testing.T) {
	got := engine.StateResetKeys("rss", "flowA", "rss_1")
	if len(got) != 1 || got[0] != cursorName("flowA", "rss_1") {
		t.Fatalf("StateResetKeys = %v, want [%q]", got, cursorName("flowA", "rss_1"))
	}
	if got[0] != "cursor.rss.flowA.rss_1" {
		t.Errorf("cursor key = %q, want cursor.rss.flowA.rss_1", got[0])
	}
	// A drop that declares no state resolves to nil — the daemon treats that
	// as "nothing to reset" (400 no_resettable_state).
	if got := engine.StateResetKeys("text", "flowA", "text_1"); got != nil {
		t.Errorf("unregistered module should have no reset keys, got %v", got)
	}
}

// TestResolveURL pins the precedence rule: a wired 'url' input beats the node's
// param, so the feed address can be computed upstream (a Text drop, a lookup)
// instead of being hardcoded on the node. An empty or non-string input falls
// through to the param rather than resolving to "" and failing as bad_param.
func TestResolveURL(t *testing.T) {
	const paramURL = "https://example.com/param.xml"
	const inputURL = "https://example.com/input.xml"

	withParam := core.Job{Params: map[string]any{"url": paramURL}}
	if got := resolveURL(withParam); got != paramURL {
		t.Errorf("param only = %q, want %q", got, paramURL)
	}

	// A wired input wins.
	wired := core.Job{
		Params: map[string]any{"url": paramURL},
		Input:  map[string]core.Ref{"url": {Inline: inputURL}},
	}
	if got := resolveURL(wired); got != inputURL {
		t.Errorf("wired input = %q, want it to beat the param", got)
	}

	// An empty wired input is treated as "not supplied" — an upstream node that
	// produced nothing must not blank out a perfectly good param.
	empty := core.Job{
		Params: map[string]any{"url": paramURL},
		Input:  map[string]core.Ref{"url": {Inline: ""}},
	}
	if got := resolveURL(empty); got != paramURL {
		t.Errorf("empty input = %q, want the param %q", got, paramURL)
	}

	// A non-string input (a number, a row list) falls through to the param.
	wrongType := core.Job{
		Params: map[string]any{"url": paramURL},
		Input:  map[string]core.Ref{"url": {Inline: 42}},
	}
	if got := resolveURL(wrongType); got != paramURL {
		t.Errorf("non-string input = %q, want the param %q", got, paramURL)
	}

	// Neither set: empty, which executeRSS reports as bad_param.
	if got := resolveURL(core.Job{}); got != "" {
		t.Errorf("nothing set = %q, want empty", got)
	}
}

// TestAllRows covers the dedupe-off projection: every feed item becomes one row
// with the full itemHeaders column set, in feed order.
func TestAllRows(t *testing.T) {
	if rows := allRows(nil); rows == nil || len(rows) != 0 {
		t.Errorf("allRows(nil) = %v, want empty non-nil", rows)
	}

	rows := allRows([]feedItem{item("A"), item("B")})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	// Feed order is preserved — a reader shows newest-first as the feed sent it.
	if rows[0]["id"] != "A" || rows[1]["id"] != "B" {
		t.Errorf("order = %v, %v; want A then B", rows[0]["id"], rows[1]["id"])
	}
	// Every declared column is present, so a downstream table has no ragged rows.
	for _, col := range itemHeaders {
		if _, ok := rows[0][col]; !ok {
			t.Errorf("row is missing the %q column", col)
		}
	}
	if len(rows[0]) != len(itemHeaders) {
		t.Errorf("row has %d keys, want exactly the %d itemHeaders", len(rows[0]), len(itemHeaders))
	}
}
