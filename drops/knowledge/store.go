// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package knowledge

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	_ "modernc.org/sqlite"
)

// The knowledge base is its own SQLite file beside the Collections one. Same
// directory, same backup, same workspace sandbox — but a separate file,
// because a table whose rows each carry six kilobytes of binary would make the
// Collections page useless to look at.
//
// There is no index. A search reads every vector in the base and scores it,
// which sounds worse than it is: the vectors are float32 and normalised on the
// way in, so scoring is a dot product, and a thousand chunks answer in tens of
// milliseconds. It stops being the right answer somewhere past twenty thousand
// chunks, which is where maxChunks refuses rather than letting a base quietly
// get slow.
const (
	knowledgeStorePath = ".dazyflow-store/knowledge.db"
	maxChunks          = 20_000
)

type chunk struct {
	source string
	ord    int
	text   string
	vector []float32
}

type hit struct {
	Source string  `json:"source"`
	Text   string  `json:"text"`
	Score  float64 `json:"score"`
}

// baseInfo is what a base remembers about how it was built. A search has to
// embed its query the same way the documents were embedded, or the numbers it
// compares mean nothing — so this is checked, not assumed.
type baseInfo struct {
	provider string
	model    string
	dim      int
}

func openStore(job core.Job, create bool) (*sql.DB, *core.Result) {
	if job.WorkspaceRoot == "" {
		r := params.Err(job, "no_sandbox", "a knowledge base needs a workspace sandbox")
		return nil, &r
	}
	root, err := os.OpenRoot(job.WorkspaceRoot)
	if err != nil {
		r := params.Err(job, "sandbox", fmt.Sprintf("open root: %v", err))
		return nil, &r
	}
	if create {
		if dir := filepath.Dir(knowledgeStorePath); dir != "" && dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				root.Close()
				r := params.Err(job, "io", fmt.Sprintf("mkdir store dir: %v", err))
				return nil, &r
			}
		}
		probe, probeErr := root.OpenFile(knowledgeStorePath, os.O_RDWR|os.O_CREATE, 0o644)
		root.Close()
		if probeErr != nil {
			r := params.Err(job, "io", fmt.Sprintf("open knowledge store: %v", probeErr))
			return nil, &r
		}
		probe.Close()
	} else {
		// Nothing has ever been added, which is empty rather than broken.
		probe, probeErr := root.Open(knowledgeStorePath)
		root.Close()
		if probeErr != nil {
			if errors.Is(probeErr, fs.ErrNotExist) {
				return nil, nil
			}
			r := params.Err(job, "io", fmt.Sprintf("open knowledge store: %v", probeErr))
			return nil, &r
		}
		probe.Close()
	}

	db, err := sql.Open("sqlite", filepath.Join(job.WorkspaceRoot, knowledgeStorePath))
	if err != nil {
		r := params.Err(job, "db", fmt.Sprintf("open knowledge store: %v", err))
		return nil, &r
	}
	if create {
		if err := ensureSchema(db); err != nil {
			db.Close()
			r := params.Err(job, "db", err.Error())
			return nil, &r
		}
	}
	return db, nil
}

func ensureSchema(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS bases (
  base     TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  model    TEXT NOT NULL,
  dim      INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS chunks (
  base     TEXT NOT NULL,
  source   TEXT NOT NULL,
  ord      INTEGER NOT NULL,
  text     TEXT NOT NULL,
  vector   BLOB NOT NULL,
  added_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS chunks_by_base ON chunks(base);
CREATE INDEX IF NOT EXISTS chunks_by_source ON chunks(base, source);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("prepare knowledge store: %w", err)
	}
	return nil
}

// isMissingTable covers the store file existing without its schema, which
// only happens if something else made the file — nothing to forget either way.
func isMissingTable(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such table")
}

func readBase(ctx context.Context, db *sql.DB, base string) (baseInfo, bool, error) {
	var info baseInfo
	err := db.QueryRowContext(ctx,
		`SELECT provider, model, dim FROM bases WHERE base = ?`, base).
		Scan(&info.provider, &info.model, &info.dim)
	if errors.Is(err, sql.ErrNoRows) {
		return baseInfo{}, false, nil
	}
	if err != nil {
		return baseInfo{}, false, err
	}
	return info, true, nil
}

func countChunks(ctx context.Context, db *sql.DB, base string) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks WHERE base = ?`, base).Scan(&n)
	return n, err
}

// replaceSource writes one document's chunks, removing whatever that source
// held before — so re-reading a page every night refreshes it instead of
// piling up a second copy of it.
func replaceSource(ctx context.Context, db *sql.DB, base string, info baseInfo, source string, chunks []chunk) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO bases (base, provider, model, dim) VALUES (?, ?, ?, ?)
		 ON CONFLICT(base) DO UPDATE SET provider = excluded.provider, model = excluded.model, dim = excluded.dim`,
		base, info.provider, info.model, info.dim); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE base = ? AND source = ?`, base, source); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format(time.RFC3339)
	for _, c := range chunks {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO chunks (base, source, ord, text, vector, added_at) VALUES (?, ?, ?, ?, ?, ?)`,
			base, source, c.ord, c.text, encodeVector(c.vector), stamp); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func forgetSource(ctx context.Context, db *sql.DB, base, source string) (int, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM chunks WHERE base = ? AND source = ?`, base, source)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// forgetBase empties a base and forgets what built it, so the name is free to
// be rebuilt with another model. Both tables in one transaction: a base row
// left behind with no passages would still refuse a query embedded by a
// different model, for a base that no longer holds anything.
func forgetBase(ctx context.Context, db *sql.DB, base string) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE base = ?`, base)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := tx.ExecContext(ctx, `DELETE FROM bases WHERE base = ?`, base); err != nil {
		return 0, err
	}
	return int(n), tx.Commit()
}

// search scores every chunk in the base against the query and returns the best
// few. Both sides are unit vectors, so the dot product IS the cosine.
func search(ctx context.Context, db *sql.DB, base string, query []float32, limit int, minScore float64) ([]hit, error) {
	rows, err := db.QueryContext(ctx, `SELECT source, text, vector FROM chunks WHERE base = ?`, base)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []hit
	for rows.Next() {
		var h hit
		var blob []byte
		if err := rows.Scan(&h.Source, &h.Text, &blob); err != nil {
			return nil, err
		}
		vec := decodeVector(blob)
		if len(vec) != len(query) {
			// A base whose dimensions disagree with the query was built with
			// another model; the caller checks for that first, so this is a
			// corrupt row rather than a configuration mistake.
			continue
		}
		h.Score = dot(query, vec)
		if h.Score < minScore {
			continue
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// normalize scales a vector to unit length so that comparing two of them is a
// dot product rather than a dot product and two square roots. A zero vector —
// which a provider should never return — is left alone and simply scores zero
// against everything.
func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

func dot(a, b []float32) float64 {
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

func encodeVector(v []float32) []byte {
	buf := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(x))
	}
	return buf
}

func decodeVector(b []byte) []float32 {
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return out
}
