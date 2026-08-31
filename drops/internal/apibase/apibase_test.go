// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package apibase

import (
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func TestNewAndGet(t *testing.T) {
	b := New("https://api.example.com")
	if got := b.Get(); got != "https://api.example.com" {
		t.Errorf("Get = %q", got)
	}
}

func TestSet(t *testing.T) {
	b := New("https://prod.example.com")
	b.Set("http://127.0.0.1:8080")
	if got := b.Get(); got != "http://127.0.0.1:8080" {
		t.Errorf("Get after Set = %q", got)
	}
}

func TestFor_DefaultWhenNoOverride(t *testing.T) {
	b := New("https://api.example.com")
	if got := b.For(core.Job{}); got != "https://api.example.com" {
		t.Errorf("For(no param) = %q, want default", got)
	}
}

func TestFor_OverrideTrimsTrailingSlash(t *testing.T) {
	b := New("https://api.example.com")
	job := core.Job{Params: map[string]any{"base_url": "https://override.test/v2/"}}
	if got := b.For(job); got != "https://override.test/v2" {
		t.Errorf("For(override) = %q, want trailing slash trimmed", got)
	}
}

func TestFor_EmptyOverrideFallsBack(t *testing.T) {
	b := New("https://api.example.com")
	job := core.Job{Params: map[string]any{"base_url": ""}}
	if got := b.For(job); got != "https://api.example.com" {
		t.Errorf("For(empty override) = %q, want default", got)
	}
}
