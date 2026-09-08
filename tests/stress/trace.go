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

func shapeOf(sql string) string {
	f := strings.Fields(strings.ToLower(strings.Join(strings.Fields(sql), " ")))
	if len(f) == 0 {
		return "(empty)"
	}
	verb := f[0]
	if verb == "with" {
		verb = "with-cte"
	}
	shape := verb
	for i, w := range f {
		switch w {
		case "from", "into", "update":
			if i+1 < len(f) {
				shape = verb + " " + strings.Trim(f[i+1], "(),")
			}
		}
		if shape != verb {
			break
		}
	}
	if verb == "select" {
		for i, w := range f {
			if w == "where" {
				return shape + " " + strings.Join(f[i+1:min(i+7, len(f))], " ")
			}
		}
	}
	return shape
}

type stmtLine struct {
	shape string
	n     int
}

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
