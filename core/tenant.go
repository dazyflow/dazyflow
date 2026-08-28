// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

type Tenant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Workspace struct {
	ID       string `json:"id"`
	TenantID string `json:"tenant_id"`
	Name     string `json:"name"`
	GitURL   string `json:"git_url"`
}
