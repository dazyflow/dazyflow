// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package rendertext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types/ref"

	"github.com/dazyflow/dazyflow/internal/rowcel"
)

type Spec struct {
	Template  string
	Column    string
	Separator string
	Prefix    string
	Suffix    string
	Empty     string
}

var ErrNoRenderer = errors.New("render_text needs either a 'template' expression or a 'column' name")

var ErrTooLarge = errors.New("rendered text exceeds the size limit")

type ParseError struct{ Err error }

func (e *ParseError) Error() string { return e.Err.Error() }
func (e *ParseError) Unwrap() error { return e.Err }

type EvalError struct{ Err error }

func (e *EvalError) Error() string { return e.Err.Error() }
func (e *EvalError) Unwrap() error { return e.Err }

// Render turns rows into one string per Spec. maxBytes <= 0 means no ceiling.
// With zero rows it returns Spec.Empty verbatim (prefix/suffix/separator do
// not apply), so an empty result set yields a chosen fallback rather than an
// empty message a sink would reject.
func Render(ctx context.Context, spec Spec, rows []map[string]any, maxBytes int) (string, error) {
	if len(rows) == 0 {
		return spec.Empty, nil
	}
	if spec.Template == "" && spec.Column == "" {
		return "", ErrNoRenderer
	}

	var prog cel.Program
	if spec.Template != "" {
		env, err := rowcel.Env()
		if err != nil {
			return "", fmt.Errorf("cel env: %w", err)
		}
		prog, err = rowcel.Compile(env, spec.Template, "template")
		if err != nil {
			return "", &ParseError{Err: err}
		}
	}

	lines := make([]string, 0, len(rows))
	for i, row := range rows {
		var line string
		if prog != nil {
			v, _, err := prog.Eval(rowcel.Vars(row))
			if err != nil {
				return "", &EvalError{Err: fmt.Errorf("template row %d: %w", i, err)}
			}
			val, err := unwrapCEL(v)
			if err != nil {
				return "", &EvalError{Err: fmt.Errorf("template row %d: %w", i, err)}
			}
			line = StringifyCell(val)
		} else {
			line = StringifyCell(row[spec.Column])
		}
		lines = append(lines, line)
	}

	out := spec.Prefix + strings.Join(lines, spec.Separator) + spec.Suffix
	if maxBytes > 0 && len(out) > maxBytes {
		return "", ErrTooLarge
	}
	return out, nil
}

// SpecFromParams builds a Spec from a drop's raw params map, applying the same
// defaults the drop uses — notably Separator defaults to a newline when the
// key is absent (an explicit "" stays empty, e.g. the HTML-table preset). Both
// the drop and the preview endpoint build their Spec through here so defaults
// can't drift between run time and preview.
func SpecFromParams(p map[string]any) Spec {
	return Spec{
		Template:  paramStringOr(p, "template", ""),
		Column:    paramStringOr(p, "column", ""),
		Separator: paramStringOr(p, "separator", "\n"),
		Prefix:    paramStringOr(p, "prefix", ""),
		Suffix:    paramStringOr(p, "suffix", ""),
		Empty:     paramStringOr(p, "empty", ""),
	}
}

func paramStringOr(p map[string]any, key, def string) string {
	if raw, ok := p[key]; ok {
		if s, ok := raw.(string); ok {
			return s
		}
	}
	return def
}

func unwrapCEL(v ref.Val) (any, error) {
	raw := v.Value()
	switch raw.(type) {
	case nil, bool, string, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64:
		return raw, nil
	}
	native, err := v.ConvertToNative(reflect.TypeOf((*any)(nil)).Elem())
	if err != nil {
		return raw, nil // fall back to the wrapped value; better than dropping
	}
	return native, nil
}

func StringifyCell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool, int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64, float32, float64:
		return fmt.Sprintf("%v", t)
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", v)
	}
}
