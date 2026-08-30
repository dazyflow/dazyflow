// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The one place a BCP-47 tag is reduced to the primary subtag Dazyflow stores
// and compares on.
//
// i18next hands out whatever the browser reported ("sv-SE", "en-GB", "EN"),
// while core.Graph.Language and the drop/template vocabularies are keyed on the
// bare subtag. Three call sites were each doing `lang.split("-")[0]` with their
// own casing rules; this is that expression, named.

export function primaryLanguage(tag: string | undefined | null): string {
  return (tag ?? "").split("-")[0].toLowerCase();
}
