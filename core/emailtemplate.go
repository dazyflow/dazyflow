// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

type EmailTemplate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	HTML string `json:"html"`
}
