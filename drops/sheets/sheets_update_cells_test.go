// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sheets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// updateServer answers the header read and captures the batchUpdate body.
func updateServer(t *testing.T, headers []any, got *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ":batchUpdate") {
			_ = json.NewDecoder(r.Body).Decode(got)
			_ = json.NewEncoder(w).Encode(map[string]any{"totalUpdatedCells": 2})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"values": [][]any{headers}})
	}))
}

// The round trip that makes "mark it done" work: rows carrying _row are
// written back to the rows they came from, under their named columns.
func TestUpdateCells_WritesBackToTheRowItCameFrom(t *testing.T) {
	var got map[string]any
	srv := updateServer(t, []any{"job", "customer", "status"}, &got)
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	res, err := executeUpdateCells(context.Background(), core.Job{
		ID: "j",
		Params: map[string]any{
			"account": "default", "spreadsheet_id": "SHEET", "range": "Jobs",
			"columns": []any{"status"},
		},
		Input: map[string]core.Ref{"rows": {Inline: []any{
			map[string]any{"_row": 5, "job": "A", "status": "Invoiced"},
			map[string]any{"_row": 9, "job": "B", "status": "Invoiced"},
		}}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q error=%+v", res.Status, res.Error)
	}

	data, _ := got["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("wrote %d ranges, want one per row: %v", len(data), got)
	}
	first, _ := data[0].(map[string]any)
	if first["range"] != "'Jobs'!C5" {
		t.Errorf("range = %v, want the status column of row 5", first["range"])
	}
	vals, _ := first["values"].([]any)
	inner, _ := vals[0].([]any)
	if inner[0] != "Invoiced" {
		t.Errorf("value = %v", inner[0])
	}
	second, _ := data[1].(map[string]any)
	if second["range"] != "'Jobs'!C9" {
		t.Errorf("second range = %v", second["range"])
	}
	if res.Output["updated_cells"].Inline != "2" {
		t.Errorf("updated_cells = %v", res.Output["updated_cells"].Inline)
	}
}

// A column the sheet doesn't have yet is added, header and all — otherwise
// the value lands in a nameless column no later read can find.
func TestUpdateCells_AddsMissingColumnWithItsHeader(t *testing.T) {
	var got map[string]any
	srv := updateServer(t, []any{"job", "customer"}, &got)
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	if _, err := executeUpdateCells(context.Background(), core.Job{
		ID:     "j",
		Params: map[string]any{"account": "default", "spreadsheet_id": "SHEET", "range": "Jobs", "columns": []any{"invoiced_on"}},
		Input: map[string]core.Ref{"rows": {Inline: []any{
			map[string]any{"_row": 4, "invoiced_on": "2026-08-20"},
		}}},
	}, nil); err != nil {
		t.Fatalf("update: %v", err)
	}
	data, _ := got["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("want a header write plus the cell, got %v", got)
	}
	hdr, _ := data[0].(map[string]any)
	if hdr["range"] != "'Jobs'!C1" {
		t.Errorf("header range = %v, want the new column's row 1", hdr["range"])
	}
	cell, _ := data[1].(map[string]any)
	if cell["range"] != "'Jobs'!C4" {
		t.Errorf("cell range = %v", cell["range"])
	}
}

// Rows without a row number can't be written back — say so plainly rather
// than writing to the wrong place.
func TestUpdateCells_MissingRowNumber(t *testing.T) {
	var got map[string]any
	srv := updateServer(t, []any{"job", "status"}, &got)
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	res, _ := executeUpdateCells(context.Background(), core.Job{
		ID:     "j",
		Params: map[string]any{"account": "default", "spreadsheet_id": "SHEET", "range": "Jobs", "columns": []any{"status"}},
		Input:  map[string]core.Ref{"rows": {Inline: []any{map[string]any{"status": "Done"}}}},
	}, nil)
	if res.Status == core.StatusOK {
		t.Fatal("a row with no _row should be refused")
	}
	if !strings.Contains(res.Error.Message, "Include row numbers") {
		t.Errorf("message should name the fix: %q", res.Error.Message)
	}
}

// Nothing to mark is a normal outcome, not a failure.
func TestUpdateCells_NoRowsIsFine(t *testing.T) {
	withSheetsEnv(t, "http://127.0.0.1:1")
	res, err := executeUpdateCells(context.Background(), core.Job{
		ID:     "j",
		Params: map[string]any{"account": "default", "spreadsheet_id": "SHEET"},
		Input:  map[string]core.Ref{"rows": {Inline: []any{}}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q error=%+v", res.Status, res.Error)
	}
	if res.Output["updated_cells"].Inline != "0" {
		t.Errorf("updated_cells = %v", res.Output["updated_cells"].Inline)
	}
}

// Read with row numbers on, and the rows know where they live.
func TestReadRange_RowNumbers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"values": [][]any{
			{"job", "status"}, {"A", "Todo"}, {"B", "Done"},
		}})
	}))
	defer srv.Close()
	withSheetsEnv(t, srv.URL)

	_, rows, err := ReadRange(context.Background(), core.Job{Params: map[string]any{
		"account": "default", "spreadsheet_id": "SHEET", "range": "Jobs", "row_numbers": true,
	}})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 2 || rows[0][RowNumberColumn] != 2 || rows[1][RowNumberColumn] != 3 {
		t.Fatalf("row numbers = %v, want 2 and 3 (row 1 is the header)", rows)
	}

	// An offset read reports where the rows really are.
	_, rows, err = ReadRange(context.Background(), core.Job{Params: map[string]any{
		"account": "default", "spreadsheet_id": "SHEET", "range": "Jobs",
		"cells": "A10:B12", "row_numbers": true,
	}})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if rows[0][RowNumberColumn] != 11 {
		t.Errorf("offset row number = %v, want 11", rows[0][RowNumberColumn])
	}
}

func TestColumnLetter(t *testing.T) {
	for i, want := range map[int]string{0: "A", 1: "B", 25: "Z", 26: "AA", 27: "AB", 51: "AZ", 52: "BA"} {
		if got := columnLetter(i); got != want {
			t.Errorf("columnLetter(%d) = %q, want %q", i, got, want)
		}
	}
}
