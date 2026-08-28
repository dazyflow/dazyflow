// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"errors"
	"testing"
)

func TestOrSelf(t *testing.T) {
	if got := orSelf(""); got != "a param value" {
		t.Errorf("orSelf(\"\") = %q", got)
	}
	if got := orSelf("params.x.y"); got != "params.x.y" {
		t.Errorf("orSelf(non-empty) = %q", got)
	}
}

func TestParamInt_AllNumericForms(t *testing.T) {
	tests := []struct {
		name   string
		v      any
		want   int
		wantOK bool
	}{
		{"float64", float64(300), 300, true},
		{"int", int(7), 7, true},
		{"int64", int64(42), 42, true},
		{"string", "5", 0, false},
		{"absent", nil, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]any{}
			if tt.v != nil {
				params["k"] = tt.v
			}
			got, ok := paramInt(params, "k")
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("paramInt = (%d, %v), want (%d, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// AuthorizeGraphRun must reject a principal who passes the tenant check but
// fails the private-flow visibility check (the previously-uncovered branch).
func TestAuthorizeGraphRun_PrivateFlowDenied(t *testing.T) {
	stranger := Principal{Subject: "u2", Tenant: "acme", Roles: []Role{{Permissions: []Permission{PermGraphRun}}}}
	priv := Graph{ID: "g", Tenant: "acme", Visibility: VisibilityPrivate, Owner: "u1"}
	if err := AuthorizeGraphRun(stranger, priv); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("private-flow run by non-owner should be unauthorized: %v", err)
	}
}
