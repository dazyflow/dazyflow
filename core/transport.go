// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "context"

type Transport interface {
	Manifest() Manifest
	Execute(ctx context.Context, job Job, progress chan<- Progress) (Result, error)
}
