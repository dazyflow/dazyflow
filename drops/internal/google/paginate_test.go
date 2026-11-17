// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package google

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

// pager serves pages of size n; total < 0 means it never stops handing out tokens.
func pager(n, total int, calls *int, wants *[]int) func(string, int) ([]int, string, error) {
	return func(tok string, want int) ([]int, string, error) {
		*calls++
		*wants = append(*wants, want)
		start := 0
		if tok != "" {
			start, _ = strconv.Atoi(tok)
		}
		var out []int
		for i := start; i < start+n && (total < 0 || i < total); i++ {
			out = append(out, i)
		}
		next := start + n
		if total >= 0 && next >= total {
			return out, "", nil
		}
		return out, strconv.Itoa(next), nil
	}
}

func TestPaginate_FollowsToEnd(t *testing.T) {
	var calls int
	var wants []int
	got, more, err := Paginate(context.Background(), 0, 10, pager(3, 7, &calls, &wants))
	if err != nil || more || len(got) != 7 || calls != 3 {
		t.Fatalf("got %v more=%v err=%v calls=%d", got, more, err, calls)
	}
	if wants[0] != 0 {
		t.Fatalf("unlimited walk should pass want=0, got %v", wants)
	}
}

func TestPaginate_StopsAtLimitAndTrims(t *testing.T) {
	var calls int
	var wants []int
	got, more, err := Paginate(context.Background(), 5, 10, pager(3, 100, &calls, &wants))
	if err != nil || !more || len(got) != 5 || calls != 2 {
		t.Fatalf("got %v more=%v err=%v calls=%d", got, more, err, calls)
	}
	if wants[0] != 5 || wants[1] != 2 {
		t.Fatalf("want = %v, expected remaining counts [5 2]", wants)
	}
}

func TestPaginate_PageCapBoundsEndlessTokens(t *testing.T) {
	var calls int
	var wants []int
	got, more, err := Paginate(context.Background(), 0, 4, pager(2, -1, &calls, &wants))
	if err != nil || !more || calls != 4 || len(got) != 8 {
		t.Fatalf("got %v more=%v err=%v calls=%d", got, more, err, calls)
	}
}

func TestPaginate_PropagatesErrors(t *testing.T) {
	boom := errors.New("boom")
	_, _, err := Paginate(context.Background(), 0, 4, func(string, int) ([]int, string, error) { return nil, "", boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = Paginate(ctx, 0, 4, func(string, int) ([]int, string, error) { return nil, "x", nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ctx err = %v", err)
	}
}
