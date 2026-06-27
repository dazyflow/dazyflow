// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "testing"

func TestCanAdminOrg(t *testing.T) {
	orgAdmin := Principal{Roles: []Role{{Name: "o", Permissions: []Permission{PermOrganizationAdmin}}}}
	platform := Principal{Roles: []Role{{Name: "p", Permissions: []Permission{PermPlatformAdmin}}}}
	editor := Principal{Roles: []Role{{Name: "e", Permissions: []Permission{PermGraphEdit, PermSecretWrite}}}}

	if !CanAdminOrg(orgAdmin) {
		t.Error("organization admin should be allowed")
	}
	if !CanAdminOrg(platform) {
		t.Error("platform admin should be allowed (superset of org admin)")
	}
	if CanAdminOrg(editor) {
		t.Error("a plain editor must not be treated as org admin")
	}
}
