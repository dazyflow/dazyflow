package notion

import (
	"strings"
	"testing"
)

func TestContentBlocks_StringBecomesParagraphs(t *testing.T) {
	blocks := contentBlocks("First paragraph.\n\nSecond paragraph.")
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	b0 := blocks[0].(map[string]any)
	if b0["type"] != "paragraph" || b0["object"] != "block" {
		t.Errorf("block0 shape = %+v", b0)
	}
	rt := b0["paragraph"].(map[string]any)["rich_text"].([]any)
	if rt[0].(map[string]any)["text"].(map[string]any)["content"] != "First paragraph." {
		t.Errorf("block0 content = %+v", rt)
	}
}

func TestContentBlocks_NilAndEmpty(t *testing.T) {
	if contentBlocks(nil) != nil {
		t.Error("nil content should yield no blocks")
	}
	if got := contentBlocks("   \n\n  "); got != nil {
		t.Errorf("whitespace-only content should yield no blocks, got %+v", got)
	}
}

func TestContentBlocks_StructuredPassesThrough(t *testing.T) {
	in := []any{map[string]any{"object": "block", "type": "divider", "divider": map[string]any{}}}
	got := contentBlocks(in)
	if len(got) != 1 || got[0].(map[string]any)["type"] != "divider" {
		t.Errorf("structured blocks should pass through untouched, got %+v", got)
	}
}

func TestRichTextChunks_SplitsLongText(t *testing.T) {
	long := strings.Repeat("a", notionContentLimit+500)
	chunks := richTextChunks(long)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}
	first := chunks[0].(map[string]any)["text"].(map[string]any)["content"].(string)
	if len([]rune(first)) != notionContentLimit {
		t.Errorf("first chunk = %d runes, want %d", len([]rune(first)), notionContentLimit)
	}
}
