// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package rss

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"
)

// feedItem is one normalized entry, dialect-agnostic: whether it came from
// RSS 2.0 <item> or Atom <entry>, downstream sees the same fields. All values
// are strings (like CSV/XML rows); Published is normalized to RFC3339 when the
// source date parses, else passed through verbatim.
type feedItem struct {
	ID        string // stable identity for dedupe: guid / atom id, else link, else title
	Title     string
	Link      string
	Published string
	Author    string
	Summary   string
	Content   string
}

// itemHeaders is the fixed column order emitted for feed rows.
var itemHeaders = []string{"id", "title", "link", "published", "author", "summary", "content"}

func (it feedItem) row() map[string]any {
	return map[string]any{
		"id":        it.ID,
		"title":     it.Title,
		"link":      it.Link,
		"published": it.Published,
		"author":    it.Author,
		"summary":   it.Summary,
		"content":   it.Content,
	}
}

// --- raw XML shapes (namespaces are matched by local name) ---

type rssRoot struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Title string `xml:"title"`
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			GUID        string `xml:"guid"`
			PubDate     string `xml:"pubDate"`
			Date        string `xml:"date"` // dc:date fallback
			Description string `xml:"description"`
			Encoded     string `xml:"encoded"` // content:encoded
			Author      string `xml:"author"`
			Creator     string `xml:"creator"` // dc:creator
		} `xml:"item"`
	} `xml:"channel"`
}

type atomRoot struct {
	XMLName xml.Name `xml:"feed"`
	Title   string   `xml:"title"`
	Entries []struct {
		Title string `xml:"title"`
		ID    string `xml:"id"`
		Links []struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
		} `xml:"link"`
		Updated   string `xml:"updated"`
		Published string `xml:"published"`
		Summary   string `xml:"summary"`
		Content   string `xml:"content"`
		Author    struct {
			Name string `xml:"name"`
		} `xml:"author"`
	} `xml:"entry"`
}

// newDecoder builds a lenient decoder: non-strict (tolerates the wild HTML-ish
// content real feeds carry) and a pass-through CharsetReader so a declared
// non-UTF-8 encoding doesn't hard-fail the parse.
func newDecoder(data []byte) *xml.Decoder {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	return dec
}

// parseFeed detects RSS 2.0 or Atom and returns its normalized items. A
// recognizable-but-empty feed yields an empty slice (not an error); only
// input that is neither dialect errors.
func parseFeed(data []byte) ([]feedItem, error) {
	var rr rssRoot
	if err := newDecoder(data).Decode(&rr); err == nil && rr.XMLName.Local == "rss" {
		return rssItems(rr), nil
	}
	var ar atomRoot
	if err := newDecoder(data).Decode(&ar); err == nil && ar.XMLName.Local == "feed" {
		return atomItems(ar), nil
	}
	return nil, fmt.Errorf("not an RSS or Atom feed (no <rss> or <feed> root with items)")
}

func rssItems(rr rssRoot) []feedItem {
	out := make([]feedItem, 0, len(rr.Channel.Items))
	for _, it := range rr.Channel.Items {
		content := firstNonEmpty(it.Encoded, it.Description)
		author := firstNonEmpty(it.Author, it.Creator)
		id := firstNonEmpty(it.GUID, it.Link, it.Title)
		out = append(out, feedItem{
			ID:        strings.TrimSpace(id),
			Title:     strings.TrimSpace(it.Title),
			Link:      strings.TrimSpace(it.Link),
			Published: normalizeDate(firstNonEmpty(it.PubDate, it.Date)),
			Author:    strings.TrimSpace(author),
			Summary:   strings.TrimSpace(it.Description),
			Content:   strings.TrimSpace(content),
		})
	}
	return out
}

func atomItems(ar atomRoot) []feedItem {
	out := make([]feedItem, 0, len(ar.Entries))
	for _, e := range ar.Entries {
		link := atomLink(e.Links)
		id := firstNonEmpty(e.ID, link, e.Title)
		out = append(out, feedItem{
			ID:        strings.TrimSpace(id),
			Title:     strings.TrimSpace(e.Title),
			Link:      strings.TrimSpace(link),
			Published: normalizeDate(firstNonEmpty(e.Published, e.Updated)),
			Author:    strings.TrimSpace(e.Author.Name),
			Summary:   strings.TrimSpace(firstNonEmpty(e.Summary, e.Content)),
			Content:   strings.TrimSpace(firstNonEmpty(e.Content, e.Summary)),
		})
	}
	return out
}

// atomLink prefers the rel="alternate" link (the human page), falling back to
// the first link with no rel (also "alternate" by spec), then any href.
func atomLink(links []struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}) string {
	for _, l := range links {
		if l.Rel == "alternate" || l.Rel == "" {
			return l.Href
		}
	}
	if len(links) > 0 {
		return links[0].Href
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// dateLayouts covers the formats feeds use: RFC 822/1123 (RSS pubDate) and
// RFC 3339 (Atom). Tried in order; first hit wins.
var dateLayouts = []string{
	time.RFC1123Z,
	time.RFC1123,
	time.RFC822Z,
	time.RFC822,
	time.RFC3339,
	"2006-01-02T15:04:05Z07:00",
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"2006-01-02",
}

// normalizeDate renders a feed date as RFC3339 (UTC) when it parses, so
// downstream comparisons/formatting see one shape; otherwise the raw string
// passes through unchanged rather than being dropped.
func normalizeDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return s
}
