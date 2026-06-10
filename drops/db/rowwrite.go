package db

import (
	"fmt"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
)

// rowwrite.go holds the validation shared by every row-writing drop —
// the {sqlite,postgres,mysql} × {insert,upsert} family. Each backend
// still parses its own connection params (path / dsn / schema) and
// supplies the dialect SQL; everything that is identical across them —
// the rows/headers inputs and the upsert conflict/update columns — lives
// here so the nine execute* functions don't each carry their own copy.
//
// The helpers return a *core.Result rather than an error: a non-nil
// result is a fully-formed bad_param/bad_input/missing_input reply the
// caller returns verbatim (`if r != nil { return *r, nil }`), matching
// the inline style these functions used before.

// rowsInput is the normalized payload of a row-writing drop: the rows to
// write plus the column headers (wired explicitly or derived from the
// rows when no headers port is connected).
type rowsInput struct {
	rows    []map[string]any
	headers []string
}

// parseRowsInput pulls the required `rows` input and optional `headers`
// input, normalizes both (accepting native typed slices and the []any /
// JSON shapes that arrive over gRPC/MCP), derives headers from the rows
// when none are wired, and validates every header is a safe identifier.
func parseRowsInput(job core.Job) (rowsInput, *core.Result) {
	rowsRef, ok := job.Input["rows"]
	if !ok {
		return rowsInput{}, errResult(job, "missing_input", "input port 'rows' is required")
	}
	rows, err := normalizeRows(rowsRef.Inline)
	if err != nil {
		return rowsInput{}, errResult(job, "bad_input", err.Error())
	}

	var headers []string
	if h, ok := job.Input["headers"]; ok && h.Inline != nil {
		headers, err = normalizeHeaders(h.Inline)
		if err != nil {
			return rowsInput{}, errResult(job, "bad_input", err.Error())
		}
	}
	if headers == nil {
		headers = deriveHeaders(rows)
	}
	for _, h := range headers {
		if err := validateIdent(h); err != nil {
			return rowsInput{}, errResult(job, "bad_input", fmt.Sprintf("column %q: %v", h, err))
		}
	}
	return rowsInput{rows: rows, headers: headers}, nil
}

// parseConflictUpdateCols validates the conflict_columns / update_columns
// params shared by the three upsert drops. updateColsExplicit preserves
// the three-mode update semantics: param absent → caller defaults to all
// non-conflict columns; explicit [] present → DO NOTHING; explicit list →
// just those columns. The distinction rides on the param's presence, not
// its length, which is why it's surfaced as a separate bool.
func parseConflictUpdateCols(job core.Job) (conflictCols, updateCols []string, updateColsExplicit bool, errRes *core.Result) {
	conflictCols, err := paramStringArray(job.Params, "conflict_columns")
	if err != nil {
		return nil, nil, false, errResult(job, "bad_param", err.Error())
	}
	if len(conflictCols) == 0 {
		return nil, nil, false, errResult(job, "bad_param", "conflict_columns must list at least one column")
	}
	for _, c := range conflictCols {
		if err := validateIdent(c); err != nil {
			return nil, nil, false, errResult(job, "bad_param", fmt.Sprintf("conflict column %q: %v", c, err))
		}
	}

	if raw, ok := job.Params["update_columns"]; ok {
		updateColsExplicit = true
		uc, err := normalizeStringArray(raw, "update_columns")
		if err != nil {
			return nil, nil, false, errResult(job, "bad_param", err.Error())
		}
		updateCols = uc
		for _, c := range updateCols {
			if err := validateIdent(c); err != nil {
				return nil, nil, false, errResult(job, "bad_param", fmt.Sprintf("update column %q: %v", c, err))
			}
		}
	}
	return conflictCols, updateCols, updateColsExplicit, nil
}

// checkConflictInHeaders rejects an upsert whose conflict column isn't
// among the row headers — there would be no value to plug into the
// conflict target. Returns nil when every conflict column is present.
func checkConflictInHeaders(job core.Job, conflictCols, headers []string) *core.Result {
	headerSet := make(map[string]struct{}, len(headers))
	for _, h := range headers {
		headerSet[h] = struct{}{}
	}
	for _, c := range conflictCols {
		if _, ok := headerSet[c]; !ok {
			return errResult(job, "bad_param", fmt.Sprintf("conflict_column %q is not in headers", c))
		}
	}
	return nil
}

// errResult builds a heap-escaped *core.Result so the parse helpers can
// signal "return this error verbatim" with a nil/non-nil pointer.
func errResult(job core.Job, code, msg string) *core.Result {
	r := params.Err(job, code, msg)
	return &r
}
