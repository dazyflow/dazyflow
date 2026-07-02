// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package value

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func runPhone(t *testing.T, params map[string]any, input map[string]core.Ref) core.Result {
	t.Helper()
	res, err := executePhone(context.Background(), core.Job{ID: "j1", Params: params, Input: input}, nil)
	if err != nil {
		t.Fatalf("executePhone: %v", err)
	}
	return res
}

func TestPhone_NormalizesSwedishLocal(t *testing.T) {
	res := runPhone(t, map[string]any{"phone": "070-123 45 67", "default_region": "SE"}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["out"].Inline; got != "+46701234567" {
		t.Errorf("out = %v, want +46701234567", got)
	}
	if got := res.Output["country"].Inline; got != "SE" {
		t.Errorf("country = %v, want SE", got)
	}
	if got := res.Output["national"].Inline; got != "701234567" {
		t.Errorf("national = %v, want 701234567", got)
	}
	if got := res.Output["type"].Inline; got != "mobile" {
		t.Errorf("type = %v, want mobile", got)
	}
}

func TestPhone_RegionDefaultsToSE(t *testing.T) {
	// No default_region param → SE default lets a local number parse.
	res := runPhone(t, map[string]any{"phone": "070-123 45 67"}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["out"].Inline; got != "+46701234567" {
		t.Errorf("out = %v, want +46701234567", got)
	}
}

func TestPhone_InternationalIgnoresRegion(t *testing.T) {
	// A +international number parses regardless of (even a wrong) default region.
	res := runPhone(t, map[string]any{"phone": "+44 20 7946 0958", "default_region": "SE"}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["country"].Inline; got != "GB" {
		t.Errorf("country = %v, want GB", got)
	}
	if got := res.Output["out"].Inline; got != "+442079460958" {
		t.Errorf("out = %v, want +442079460958", got)
	}
}

func TestPhone_WiredInputWins(t *testing.T) {
	res := runPhone(t,
		map[string]any{"phone": "+44 20 7946 0958", "default_region": "SE"},
		map[string]core.Ref{"phone": {MIME: "text/plain", Inline: "070-123 45 67"}},
	)
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["out"].Inline; got != "+46701234567" {
		t.Errorf("out = %v, want the wired Swedish number normalized", got)
	}
}

func TestPhone_InvalidFailsNode(t *testing.T) {
	// Digit-shaped but not a real number → bad_param, not a passed-through value.
	res := runPhone(t, map[string]any{"phone": "12345", "default_region": "SE"}, nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result for an invalid number")
	}
	if res.Error == nil || res.Error.Code != "bad_param" {
		t.Errorf("error = %+v, want bad_param", res.Error)
	}
}

func TestPhone_Required(t *testing.T) {
	res := runPhone(t, map[string]any{"default_region": "SE"}, nil)
	if res.Status == core.StatusOK {
		t.Fatal("want an error result when phone is empty")
	}
}
