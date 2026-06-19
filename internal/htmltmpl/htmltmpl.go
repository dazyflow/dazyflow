// Package htmltmpl is the single HTML-template render engine shared by the
// render_template drop (drops/transform) and the editor's live-preview
// endpoint (daemon). Keeping the parse + safe FuncMap + output cap in one
// place guarantees the preview a user sees while editing is byte-identical
// to what the flow actually sends at run time — no second implementation to
// drift.
//
// The engine is Go's html/template, chosen for context-aware auto-escaping:
// merge values are escaped for their HTML context, so untrusted data can't
// inject markup. Templates can't reach the filesystem, network, or process —
// only the funcs below.
package htmltmpl

import (
	"errors"
	"fmt"
	"html/template"
	"strings"
)

// DefaultMaxBytes caps rendered output. html/template has no built-in size
// or time budget, so a template that ranges over a large input (or expands
// explosively) could otherwise allocate unbounded memory. Far above any real
// email body, but turns a runaway render into a clean error instead of an OOM.
const DefaultMaxBytes = 8 << 20 // 8 MiB

// ErrTooLarge is returned (wrapped) when a render exceeds the byte cap.
var ErrTooLarge = errors.New("rendered output exceeds the size limit")

// ParseError wraps a template-parse failure (an authoring mistake: a bad
// action or mismatched {{ }}), so callers can distinguish it from an
// execution error and map it to the right error code / message.
type ParseError struct{ Err error }

func (e *ParseError) Error() string { return e.Err.Error() }
func (e *ParseError) Unwrap() error { return e.Err }

// Funcs is the deliberately-small, side-effect-free helper set exposed to
// templates. Pure string/JSON ops only — nothing that touches the fs,
// network, or process — so an authored template can't escape the render.
var Funcs = template.FuncMap{
	// default returns fallback when v is nil or an empty string; else v.
	// Usage: {{.name | default "there"}}
	"default": func(fallback, v any) any {
		if v == nil {
			return fallback
		}
		if s, ok := v.(string); ok && s == "" {
			return fallback
		}
		return v
	},
	"upper": strings.ToUpper,
	"lower": strings.ToLower,
	// join concatenates a list with sep; accepts []string or []any.
	"join": func(sep string, list any) string {
		switch xs := list.(type) {
		case []string:
			return strings.Join(xs, sep)
		case []any:
			parts := make([]string, len(xs))
			for i, x := range xs {
				parts[i] = fmt.Sprintf("%v", x)
			}
			return strings.Join(parts, sep)
		default:
			return fmt.Sprintf("%v", list)
		}
	},
}

// Render parses tmplText as an HTML template, executes it against data, and
// returns the rendered HTML. maxBytes <= 0 uses DefaultMaxBytes. Errors:
//   - *ParseError       — the template text is invalid
//   - wraps ErrTooLarge — the output exceeded the cap
//   - any other error   — an execution failure (bad range operand, missing
//     method, etc.)
//
// Auto-escaping is the injection defense: data values are escaped for their
// HTML context, and data is NEVER re-parsed as a template (no second-order
// injection).
func Render(tmplText string, data any, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	tmpl, err := template.New("render").Funcs(Funcs).Parse(tmplText)
	if err != nil {
		return "", &ParseError{Err: err}
	}
	var buf strings.Builder
	lw := &limitedWriter{w: &buf, limit: maxBytes}
	if err := tmpl.Execute(lw, data); err != nil {
		if lw.tripped {
			return "", fmt.Errorf("%w (%d bytes)", ErrTooLarge, maxBytes)
		}
		return "", err
	}
	return buf.String(), nil
}

// limitedWriter caps how many bytes a render may produce. Past the limit
// Write errors (tripping Execute) and records tripped so Render can tell a
// size overflow from an ordinary template error.
type limitedWriter struct {
	w       *strings.Builder
	limit   int
	written int
	tripped bool
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.written+len(p) > l.limit {
		l.tripped = true
		return 0, fmt.Errorf("output limit exceeded")
	}
	n, err := l.w.Write(p)
	l.written += n
	return n, err
}
