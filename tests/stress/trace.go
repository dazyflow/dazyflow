// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package stress

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
)

// stmtCounter counts the statements a run actually issues, grouped by shape.
//
// "13 transactions per step" is a number to act on only once you know which
// thirteen. Postgres can be asked with pg_stat_statements, but that needs a
// preloaded library and a server restart; the driver already sees every
// statement, so ask it instead.
type stmtCounter struct {
	mu    sync.Mutex
	count map[string]int
}

func newStmtCounter() *stmtCounter { return &stmtCounter{count: map[string]int{}} }

func (c *stmtCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	c.count[shapeOf(d.SQL)]++
	c.mu.Unlock()
	return ctx
}

func (c *stmtCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// shapeOf reduces a statement to something readable and stable: the verb plus
// the table it touches, which is all that is needed to see where the round
// trips go.
func shapeOf(sql string) string {
	f := strings.Fields(strings.ToLower(strings.Join(strings.Fields(sql), " ")))
	if len(f) == 0 {
		return "(empty)"
	}
	verb := f[0]
	if verb == "with" {
		verb = "with-cte"
	}
	for i, w := range f {
		switch w {
		case "from", "into", "update":
			if i+1 < len(f) {
				return verb + " " + strings.Trim(f[i+1], "(),")
			}
		}
	}
	return verb
}

type stmtLine struct {
	shape string
	n     int
}

// top returns the statement shapes by frequency, most first.
func (c *stmtCounter) top() []stmtLine {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]stmtLine, 0, len(c.count))
	for shape, n := range c.count {
		out = append(out, stmtLine{shape, n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].n > out[j].n })
	return out
}

func (c *stmtCounter) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.count {
		n += v
	}
	return n
}

func (c *stmtCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count = map[string]int{}
}
