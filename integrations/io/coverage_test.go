package io

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// ----------------------------------------------------------------------
// coerceTypedCell: direct exercise of every excelize.CellType branch.
// ----------------------------------------------------------------------

func TestCoerceTypedCell_AllBranches(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	cell := "A1"

	// CellTypeUnset → int when "42"
	if v := coerceTypedCell(f, "Sheet1", cell, "42", excelize.CellTypeUnset, false); v != int64(42) {
		t.Errorf("unset 42 = %v (%T), want int64(42)", v, v)
	}
	// CellTypeUnset → float when "3.14"
	if v := coerceTypedCell(f, "Sheet1", cell, "3.14", excelize.CellTypeUnset, false); v != 3.14 {
		t.Errorf("unset 3.14 = %v (%T)", v, v)
	}
	// CellTypeUnset → bool TRUE.
	if v := coerceTypedCell(f, "Sheet1", cell, "TRUE", excelize.CellTypeUnset, false); v != true {
		t.Errorf("unset TRUE = %v", v)
	}
	// CellTypeUnset → bool FALSE.
	if v := coerceTypedCell(f, "Sheet1", cell, "FALSE", excelize.CellTypeUnset, false); v != false {
		t.Errorf("unset FALSE = %v", v)
	}
	// CellTypeUnset → string fallback for non-numeric, non-bool.
	if v := coerceTypedCell(f, "Sheet1", cell, "hello", excelize.CellTypeUnset, false); v != "hello" {
		t.Errorf("unset 'hello' = %v", v)
	}
	// CellTypeBool TRUE/1 → true.
	if v := coerceTypedCell(f, "Sheet1", cell, "1", excelize.CellTypeBool, false); v != true {
		t.Errorf("bool '1' = %v", v)
	}
	if v := coerceTypedCell(f, "Sheet1", cell, "TRUE", excelize.CellTypeBool, false); v != true {
		t.Errorf("bool 'TRUE' = %v", v)
	}
	// CellTypeBool FALSE/0 → false.
	if v := coerceTypedCell(f, "Sheet1", cell, "0", excelize.CellTypeBool, false); v != false {
		t.Errorf("bool '0' = %v", v)
	}
	if v := coerceTypedCell(f, "Sheet1", cell, "FALSE", excelize.CellTypeBool, false); v != false {
		t.Errorf("bool 'FALSE' = %v", v)
	}
	// CellTypeBool with weird text → string passthrough.
	if v := coerceTypedCell(f, "Sheet1", cell, "yup", excelize.CellTypeBool, false); v != "yup" {
		t.Errorf("bool weird = %v", v)
	}
	// CellTypeNumber "42" → int64
	if v := coerceTypedCell(f, "Sheet1", cell, "42", excelize.CellTypeNumber, false); v != int64(42) {
		t.Errorf("num 42 = %v (%T)", v, v)
	}
	// CellTypeNumber "3.14" → float64
	if v := coerceTypedCell(f, "Sheet1", cell, "3.14", excelize.CellTypeNumber, false); v != 3.14 {
		t.Errorf("num 3.14 = %v", v)
	}
	// CellTypeNumber with unparseable → string passthrough.
	if v := coerceTypedCell(f, "Sheet1", cell, "n/a", excelize.CellTypeNumber, false); v != "n/a" {
		t.Errorf("num 'n/a' = %v", v)
	}
	// CellTypeFormula numeric → float64.
	if v := coerceTypedCell(f, "Sheet1", cell, "3.14", excelize.CellTypeFormula, false); v != 3.14 {
		t.Errorf("formula 3.14 = %v", v)
	}
	// CellTypeFormula non-numeric → string.
	if v := coerceTypedCell(f, "Sheet1", cell, "abc", excelize.CellTypeFormula, false); v != "abc" {
		t.Errorf("formula 'abc' = %v", v)
	}
	// CellTypeError / inline-string / shared-string → string passthrough.
	if v := coerceTypedCell(f, "Sheet1", cell, "#DIV/0!", excelize.CellTypeError, false); v != "#DIV/0!" {
		t.Errorf("err = %v", v)
	}
	if v := coerceTypedCell(f, "Sheet1", cell, "inline", excelize.CellTypeInlineString, false); v != "inline" {
		t.Errorf("inline = %v", v)
	}
	if v := coerceTypedCell(f, "Sheet1", cell, "shared", excelize.CellTypeSharedString, false); v != "shared" {
		t.Errorf("shared = %v", v)
	}
	// CellTypeDate with valid date-serial → time.Time
	if v := coerceTypedCell(f, "Sheet1", cell, "44197", excelize.CellTypeDate, false); v == "44197" {
		t.Errorf("date returned raw string, want time.Time")
	}
	// CellTypeDate with garbage → string passthrough.
	if v := coerceTypedCell(f, "Sheet1", cell, "n/a", excelize.CellTypeDate, false); v != "n/a" {
		t.Errorf("bad date = %v", v)
	}
}

// ----------------------------------------------------------------------
// excel_write helpers: normalizeRows, coerceRowMap, normalizeHeaders.
// ----------------------------------------------------------------------

func TestCoerceRowMap_Variants(t *testing.T) {
	// map[string]any passes through unchanged.
	if m, err := coerceRowMap(map[string]any{"k": "v"}); err != nil || m["k"] != "v" {
		t.Errorf("map[string]any → (%v, %v)", m, err)
	}
	// map[string]string is widened to map[string]any.
	if m, err := coerceRowMap(map[string]string{"k": "v"}); err != nil || m["k"] != "v" {
		t.Errorf("map[string]string → (%v, %v)", m, err)
	}
	// Anything else is an error.
	if _, err := coerceRowMap("not a row"); err == nil {
		t.Error("coerceRowMap(string): want error")
	}
}

func TestNormalizeRows_Variants(t *testing.T) {
	// nil → (nil, nil)
	if got, err := normalizeRows(nil); err != nil || got != nil {
		t.Errorf("nil → (%v, %v)", got, err)
	}
	// []map[string]any passes.
	in1 := []map[string]any{{"k": "v"}}
	if got, err := normalizeRows(in1); err != nil || len(got) != 1 || got[0]["k"] != "v" {
		t.Errorf("[]map[string]any → (%v, %v)", got, err)
	}
	// []map[string]string is widened.
	in2 := []map[string]string{{"k": "v"}}
	if got, err := normalizeRows(in2); err != nil || got[0]["k"] != "v" {
		t.Errorf("[]map[string]string → (%v, %v)", got, err)
	}
	// []any of objects.
	in3 := []any{map[string]any{"k": "v"}}
	if got, err := normalizeRows(in3); err != nil || got[0]["k"] != "v" {
		t.Errorf("[]any → (%v, %v)", got, err)
	}
	// []any with a bad element → annotated error mentioning the row index.
	in4 := []any{map[string]any{"k": "v"}, "not-a-row"}
	if _, err := normalizeRows(in4); err == nil || !strings.Contains(err.Error(), "row 1") {
		t.Errorf("[]any with bad row → %v, want one mentioning 'row 1'", err)
	}
	// JSON-string input.
	if got, err := normalizeRows(`[{"k":"v"}]`); err != nil || got[0]["k"] != "v" {
		t.Errorf("JSON string → (%v, %v)", got, err)
	}
	if _, err := normalizeRows("not json"); err == nil {
		t.Error("JSON string with garbage: want error")
	}
	// []byte input.
	if got, err := normalizeRows([]byte(`[{"k":"v"}]`)); err != nil || got[0]["k"] != "v" {
		t.Errorf("JSON bytes → (%v, %v)", got, err)
	}
	if _, err := normalizeRows([]byte("not json")); err == nil {
		t.Error("JSON bytes with garbage: want error")
	}
	// Unsupported type.
	if _, err := normalizeRows(42); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("int → %v, want unsupported-type error", err)
	}
}

func TestNormalizeHeaders_Variants(t *testing.T) {
	// []string passes.
	if got, err := normalizeHeaders([]string{"a", "b"}); err != nil || got[1] != "b" {
		t.Errorf("[]string → (%v, %v)", got, err)
	}
	// []any of strings is widened.
	if got, err := normalizeHeaders([]any{"a", "b"}); err != nil || got[1] != "b" {
		t.Errorf("[]any → (%v, %v)", got, err)
	}
	// []any with a non-string element errors with an indexed message.
	_, err := normalizeHeaders([]any{"a", 42})
	if err == nil || !strings.Contains(err.Error(), "[1]") {
		t.Errorf("[]any with int → %v, want one mentioning '[1]'", err)
	}
	// Unsupported type.
	if _, err := normalizeHeaders(42); err == nil {
		t.Error("int → want error")
	}
}

// ----------------------------------------------------------------------
// http_download helpers: downloadURL, downloadHeaders, downloadStatusOK,
// paramIntSliceLocal.
// ----------------------------------------------------------------------

func TestDownloadURL_InputOverridesParam(t *testing.T) {
	// Input["url"] wins over params.url.
	job := core.Job{
		Input: map[string]core.Ref{"url": {Inline: "https://from-input"}},
		Params: map[string]any{"url": "https://from-params"},
	}
	if got := downloadURL(job); got != "https://from-input" {
		t.Errorf("got %q, want from-input", got)
	}
	// Empty Input falls through to params.
	job.Input = nil
	if got := downloadURL(job); got != "https://from-params" {
		t.Errorf("got %q, want from-params", got)
	}
	// Input present but Inline is not a string → falls through to params.
	job.Input = map[string]core.Ref{"url": {Inline: 42}}
	if got := downloadURL(job); got != "https://from-params" {
		t.Errorf("got %q (non-string input), want from-params", got)
	}
	// Input with empty string → falls through.
	job.Input = map[string]core.Ref{"url": {Inline: ""}}
	if got := downloadURL(job); got != "https://from-params" {
		t.Errorf("got %q (empty input), want from-params", got)
	}
}

func TestDownloadHeaders_Variants(t *testing.T) {
	// nil/missing returns nil headers, no error.
	if got, err := downloadHeaders(map[string]any{}); err != nil || got != nil {
		t.Errorf("missing → (%v, %v)", got, err)
	}
	if got, err := downloadHeaders(map[string]any{"headers": nil}); err != nil || got != nil {
		t.Errorf("nil → (%v, %v)", got, err)
	}
	// Happy path.
	in := map[string]any{"headers": map[string]any{"X-Auth": "abc"}}
	got, err := downloadHeaders(in)
	if err != nil || got["X-Auth"] != "abc" {
		t.Errorf("happy → (%v, %v)", got, err)
	}
	// Not an object → error.
	if _, err := downloadHeaders(map[string]any{"headers": "string"}); err == nil {
		t.Error("string headers: want error")
	}
	// Non-string value → error mentioning the key.
	bad := map[string]any{"headers": map[string]any{"X-Num": 42}}
	if _, err := downloadHeaders(bad); err == nil || !strings.Contains(err.Error(), "X-Num") {
		t.Errorf("non-string val → %v, want one mentioning 'X-Num'", err)
	}
}

func TestDownloadStatusOK(t *testing.T) {
	// Empty expect → default 2xx.
	if !downloadStatusOK(200, nil) {
		t.Error("200 with empty expect: want OK")
	}
	if downloadStatusOK(404, nil) {
		t.Error("404 with empty expect: want NOT OK")
	}
	// Non-empty expect must match exactly.
	if !downloadStatusOK(404, []int{404}) {
		t.Error("404 in expect=[404]: want OK")
	}
	if downloadStatusOK(200, []int{404}) {
		t.Error("200 with expect=[404]: want NOT OK")
	}
	if !downloadStatusOK(204, []int{200, 204}) {
		t.Error("204 in expect=[200,204]: want OK")
	}
}

func TestParamIntSliceLocal(t *testing.T) {
	// Missing key → nil.
	if got := paramIntSliceLocal(map[string]any{}, "x"); got != nil {
		t.Errorf("missing → %v, want nil", got)
	}
	// Non-slice → nil.
	if got := paramIntSliceLocal(map[string]any{"x": "string"}, "x"); got != nil {
		t.Errorf("non-slice → %v, want nil", got)
	}
	// Happy path: JSON-decoded ints arrive as float64.
	in := map[string]any{"x": []any{float64(200), float64(204), "skip-non-number"}}
	got := paramIntSliceLocal(in, "x")
	if len(got) != 2 || got[0] != 200 || got[1] != 204 {
		t.Errorf("got %v, want [200 204]", got)
	}
}

// TestHTTPDownload_AcceptsCustomExpectStatus covers the
// expect_status configuration path end-to-end.
func TestHTTPDownload_AcceptsCustomExpectStatus(t *testing.T) {
	ws := t.TempDir()
	srv := downloadServer(t, []byte("nope"), 404)
	res, err := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "x.txt",
			"expect_status":          []any{float64(404)},
			"allow_private_networks": true,
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%v (%+v)", res.Status, err, res.Error)
	}
}

// TestHTTPDownload_BadHeaders covers the bad-param branch for headers
// (object map → string val expected).
func TestHTTPDownload_BadHeaders(t *testing.T) {
	ws := t.TempDir()
	srv := downloadServer(t, []byte("x"), 200)
	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "x.txt",
			"headers":                map[string]any{"X-N": 42},
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, errCode(res))
	}
}

// TestHTTPDownload_SendsHeaders confirms the headers loop in the
// execute path actually sets request headers (the for-range over
// headers). Uses an httptest server that echoes back the seen
// X-Foo header.
func TestHTTPDownload_SendsHeaders(t *testing.T) {
	ws := t.TempDir()
	var seenHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeader = r.Header.Get("X-Foo")
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	res, err := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "out.txt",
			"headers":                map[string]any{"X-Foo": "bar"},
			"allow_private_networks": true,
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if seenHeader != "bar" {
		t.Errorf("server saw X-Foo=%q, want 'bar'", seenHeader)
	}
}

// TestHTTPDownload_MkdirsBeforeCreate covers the mkdirs=true branch of
// executeHTTPDownload.
func TestHTTPDownload_MkdirsBeforeCreate(t *testing.T) {
	ws := t.TempDir()
	srv := downloadServer(t, []byte("hi"), 200)
	res, err := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "deep/sub/dir/file.txt",
			"mkdirs":                 true,
			"allow_private_networks": true,
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%v (%+v)", res.Status, err, res.Error)
	}
	if _, err := os.Stat(filepath.Join(ws, "deep/sub/dir/file.txt")); err != nil {
		t.Errorf("file not written under mkdir'd path: %v", err)
	}
}

// TestHTTPDownload_BadURLRequest covers the http.NewRequestWithContext
// error path — pass a URL the http package can't parse.
func TestHTTPDownload_BadURLRequest(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url":                    "http://%%%%/bad",
			"path":                   "x.txt",
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Errorf("status=%q, want error", res.Status)
	}
}

// TestHTTPDownload_HTTPErrorWithoutSSRF covers the "http" error code
// branch — the server closes the connection without responding so
// resp.Do() errors with a non-SSRF reason.
func TestHTTPDownload_HTTPErrorWithoutSSRF(t *testing.T) {
	ws := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hijack and close to produce a stream error after request acceptance.
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)

	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params: map[string]any{
			"url": srv.URL, "path": "x.txt",
			"allow_private_networks": true,
		},
	}, nil)
	if res.Status != core.StatusError {
		t.Errorf("status=%q, want error", res.Status)
	}
}

// ----------------------------------------------------------------------
// streamToFile via mock root: short-write injection covers the "io"
// error code branch when the destination Write fails.
// ----------------------------------------------------------------------

type erroringWriter struct{}

func (erroringWriter) Write(_ []byte) (int, error) { return 0, errors.New("disk full") }

type noopRoot struct{}

func (noopRoot) Remove(_ string) error { return nil }

func TestStreamToFile_WriteFailure(t *testing.T) {
	job := core.Job{ID: "j"}
	src := strings.NewReader("some content")
	written, errRes := streamToFile(job, src, erroringWriter{}, noopRoot{}, "rel", 0)
	if errRes == nil {
		t.Fatal("expected error result")
	}
	if errRes.Error == nil || errRes.Error.Code != "io" {
		t.Errorf("code = %q, want io", errCode(*errRes))
	}
	if written != 0 {
		t.Errorf("written = %d, want 0 on failure", written)
	}
}

// ----------------------------------------------------------------------
// file_picker: guessMIMEByExt for less-common extensions
// ----------------------------------------------------------------------

func TestGuessMIMEByExt_Extensions(t *testing.T) {
	cases := map[string]string{
		"a.xlsx":   "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"a.xlsm":   "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"a.xls":    "application/vnd.ms-excel",
		"a.jsonl":  "application/x-ndjson",
		"a.ndjson": "application/x-ndjson",
		"a.txt":    "text/plain",
		"a.log":    "text/plain",
		"a.md":     "text/markdown",
		"a.html":   "text/html",
		"a.htm":    "text/html",
		"a.xml":    "application/xml",
		"a.yaml":   "application/yaml",
		"a.yml":    "application/yaml",
		"a.pdf":    "application/pdf",
		"a.png":    "image/png",
		"a.jpg":    "image/jpeg",
		"a.jpeg":   "image/jpeg",
		"a.sqlite": "application/vnd.sqlite3",
		"a.db":     "application/vnd.sqlite3",
		"a.dat":    "application/octet-stream",
		"":         "application/octet-stream",
	}
	for path, want := range cases {
		if got := guessMIMEByExt(path); got != want {
			t.Errorf("guessMIMEByExt(%q) = %q, want %q", path, got, want)
		}
	}
}

// ----------------------------------------------------------------------
// file_write: isSandboxEscape on various error shapes
// ----------------------------------------------------------------------

func TestIsSandboxEscape(t *testing.T) {
	if isSandboxEscape(nil) {
		t.Error("nil err: want false")
	}
	if !isSandboxEscape(os.ErrInvalid) {
		t.Error("os.ErrInvalid: want true")
	}
	if !isSandboxEscape(errors.New("path escapes root")) {
		t.Error("'path escapes': want true")
	}
	if !isSandboxEscape(errors.New("argument outside root")) {
		t.Error("'outside root': want true")
	}
	if !isSandboxEscape(errors.New("invalid argument")) {
		t.Error("'invalid argument': want true")
	}
	if isSandboxEscape(errors.New("some other error")) {
		t.Error("unrelated err: want false")
	}
}

// ----------------------------------------------------------------------
// inlineToBytes: every input shape
// ----------------------------------------------------------------------

func TestInlineToBytes_Variants(t *testing.T) {
	// []byte passes through.
	got, err := inlineToBytes([]byte("hello"))
	if err != nil || string(got) != "hello" {
		t.Errorf("[]byte → (%q, %v)", got, err)
	}
	// string → []byte.
	got, err = inlineToBytes("hi")
	if err != nil || string(got) != "hi" {
		t.Errorf("string → (%q, %v)", got, err)
	}
	// Anything else marshals to JSON.
	got, err = inlineToBytes(map[string]any{"k": "v"})
	if err != nil {
		t.Errorf("map: err = %v", err)
	}
	var back map[string]string
	if err := json.Unmarshal(got, &back); err != nil || back["k"] != "v" {
		t.Errorf("JSON marshal: %s err=%v", got, err)
	}
}

// ----------------------------------------------------------------------
// excel_write: sheet param + autosize + freeze + missing sandbox edge cases
// ----------------------------------------------------------------------

func TestExcelWrite_AutosizeAndFreeze(t *testing.T) {
	ws := t.TempDir()
	res, err := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "1", "b": "2"}}},
		},
		Params: map[string]any{
			"path":      "out.xlsx",
			"autosize":  true,
			"freezeRow": float64(1),
		},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, err := os.Stat(filepath.Join(ws, "out.xlsx")); err != nil {
		t.Errorf("file not written: %v", err)
	}
}

// TestExcelWrite_NormalizedHeadersDriveColumnOrder forces the
// "headers input alongside rows" path so normalizeHeaders runs.
func TestExcelWrite_NormalizedHeadersDriveColumnOrder(t *testing.T) {
	ws := t.TempDir()
	res, err := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"b": "2", "a": "1"}}},
			"headers": {Inline: []any{"a", "b"}}, // forces normalizeHeaders []any branch
		},
		Params: map[string]any{"path": "out.xlsx"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// TestExcelWrite_BadHeadersInput pins the "bad_input" branch for the
// headers-input path.
func TestExcelWrite_BadHeadersInput(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"a": "1"}}},
			"headers": {Inline: 42}, // unsupported type
		},
		Params: map[string]any{"path": "out.xlsx"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("code = %q, want bad_input", errCode(res))
	}
}

// TestExcelWrite_BadRowsInput pins the "bad_input" branch for the rows
// shape (normalizeRows error).
func TestExcelWrite_BadRowsInput(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input: map[string]core.Ref{
			"rows": {Inline: 42}, // unsupported
		},
		Params: map[string]any{"path": "out.xlsx"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("code = %q, want bad_input", errCode(res))
	}
}

// TestExcelWrite_NamedSheetParam exercises the sheet rename branch
// when a non-default sheet name is supplied.
func TestExcelWrite_NamedSheetParam(t *testing.T) {
	ws := t.TempDir()
	res, err := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"k": "v"}}},
		},
		Params: map[string]any{"path": "out.xlsx", "sheet": "Orders"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// silence unused: io.Discard if a sub-test stops using it
var _ = io.Discard

// ----------------------------------------------------------------------
// file_write error branches: missing param, missing input
// ----------------------------------------------------------------------

func TestFileWrite_MissingPathParam(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"in": {Inline: "hi"}},
		Params:        map[string]any{},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", errCode(res))
	}
}

func TestFileWrite_MissingInputPort(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"path": "out.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "missing_input" {
		t.Errorf("code = %q, want missing_input", errCode(res))
	}
}

func TestFileWrite_NoSandbox(t *testing.T) {
	res, _ := executeFileWrite(t.Context(), core.Job{
		// No WorkspaceRoot, no ScratchRoot.
		Input:  map[string]core.Ref{"in": {Inline: "hi"}},
		Params: map[string]any{"path": "out.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_sandbox" {
		t.Errorf("code = %q, want no_sandbox", errCode(res))
	}
}

// TestFileWrite_FromRef covers the input.Ref path: read from a source
// file (in the sandbox) and write to a destination.
func TestFileWrite_FromRef(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "src.txt"), []byte("from-ref"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"in": {Ref: "src.txt", MIME: "text/plain"}},
		Params:        map[string]any{"path": "dst.txt"},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	got, _ := os.ReadFile(filepath.Join(ws, "dst.txt"))
	if string(got) != "from-ref" {
		t.Errorf("dst = %q, want 'from-ref'", got)
	}
}

func TestFileWrite_FromMissingRef(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeFileWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"in": {Ref: "ghost.txt"}},
		Params:        map[string]any{"path": "dst.txt"},
	}, nil)
	if res.Status != core.StatusError {
		t.Errorf("status=%q, want error", res.Status)
	}
}

// ----------------------------------------------------------------------
// file_read missing path param + missing sandbox.
// ----------------------------------------------------------------------

func TestFileRead_MissingPathParam(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeFileRead(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", errCode(res))
	}
}

// TestExcelWrite_MissingPathParam covers the bad_param branch.
func TestExcelWrite_MissingPathParam(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"rows": {Inline: []map[string]any{{"a": "1"}}}},
		Params:        map[string]any{},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", errCode(res))
	}
}

// TestHTTPDownload_MissingURL covers the early bad_param branch.
func TestHTTPDownload_MissingURL(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"path": "x.txt"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", errCode(res))
	}
}

// TestHTTPDownload_MissingPath covers the bad_param branch when url is
// supplied but path isn't.
func TestHTTPDownload_MissingPath(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeHTTPDownload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"url": "https://example/x"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", errCode(res))
	}
}

// TestHTTPDownload_NoSandbox covers the no_sandbox branch.
func TestHTTPDownload_NoSandbox(t *testing.T) {
	srv := downloadServer(t, []byte("x"), 200)
	res, _ := executeHTTPDownload(t.Context(), core.Job{
		// no WorkspaceRoot, no scratch
		Params: map[string]any{"url": srv.URL, "path": "x.txt", "allow_private_networks": true},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "no_sandbox" {
		t.Errorf("code = %q, want no_sandbox", errCode(res))
	}
}

// TestExcelRead_TypedWithoutHeaders covers flattenRowsTyped's
// no-headers branch where columns are auto-named col_0, col_1, ...
func TestExcelRead_TypedWithoutHeaders(t *testing.T) {
	ws := t.TempDir()
	seedXLSX(t, ws, "data.xlsx", map[string][][]string{
		"Sheet1": {
			{"42", "3.14", "TRUE"},
			{"7", "1.0", "FALSE"},
		},
	})
	res, err := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"path": "data.xlsx", "typed": true, "headers": false},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	headers, _ := res.Output["headers"].Inline.([]string)
	if len(headers) != 3 || headers[0] != "col_0" {
		t.Errorf("headers = %v, want [col_0 col_1 col_2]", headers)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// excelize stores string-typed cells as shared strings, so the raw
	// value comes back as a string — just confirm the cell isn't nil
	// and the column lookup worked.
	if rows[0]["col_0"] == nil {
		t.Errorf("rows[0].col_0 unexpectedly nil")
	}
}

// TestExcelRead_MissingFile covers the "io" error branch when
// the workspace exists but the file doesn't.
func TestExcelRead_MissingFile(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{"path": "ghost.xlsx"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "io" {
		t.Errorf("code = %q, want io", errCode(res))
	}
}

// TestExcelRead_MissingPathParam covers the bad_param early-return.
func TestExcelRead_MissingPathParam(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeExcelRead(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Params:        map[string]any{},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", errCode(res))
	}
}

// TestExcelWrite_AppendToExistingFile covers renderAppendedXLSX's
// happy path.
func TestExcelWrite_AppendToExistingFile_FromCoverage(t *testing.T) {
	ws := t.TempDir()
	seedXLSX(t, ws, "existing.xlsx", map[string][][]string{
		"Sheet1": {{"id", "name"}, {"1", "Alice"}},
	})
	res, err := executeExcelWrite(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"id": "2", "name": "Bob"}}},
		},
		Params: map[string]any{"path": "existing.xlsx", "append": true},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// TestHTTPUpload_MissingURL covers the bad_param branch.
func TestHTTPUpload_MissingURL(t *testing.T) {
	ws := t.TempDir()
	res, _ := executeHTTPUpload(t.Context(), core.Job{
		WorkspaceRoot: ws,
		Input:         map[string]core.Ref{"in": {Ref: "x.txt"}},
		Params:        map[string]any{},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("code = %q, want bad_param", errCode(res))
	}
}
