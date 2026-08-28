// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// What a node card shows about the language a text field is written in.
//
// A step says which language it is in a param of its own — Text's "Written in",
// the runner step's "Run it with" — and the field points at it with
// x_lang_param. On the canvas that fact is worth showing: a Text node holding a
// SQL query and one holding an email body are different nodes to a reader, and
// they used to look identical.
//
// The glyph says what KIND of thing it is, not which language it is. Only three
// of the languages on offer have a mark anyone would recognise (Python,
// JavaScript, PowerShell) — SQL is a standard rather than a product, and YAML
// and shell have no logo at all — so a row of real brand marks beside invented
// ones would read as broken. A terminal, a database, a pair of braces: those
// group the seven honestly, and the label carries the identity.

// LangGlyph is the shape of thing, which is what a small icon can carry.
export type LangGlyph = "terminal" | "database" | "braces" | "code" | "text";

// glyphFor maps a language onto its kind. Unknown values get the generic code
// glyph rather than nothing: a flow built by the API can carry anything here,
// and a chip with a label and no icon looks like a rendering bug.
export function glyphFor(lang: string): LangGlyph {
  switch (lang) {
    case "shell":
    case "sh":
    case "bash":
    case "powershell":
      return "terminal";
    case "sql":
      return "database";
    case "json":
      return "braces";
    case "yaml":
    case "yml":
      return "text";
    default:
      return "code";
  }
}

// languageOf reads the language a field is written in, or "" when the node has
// not chosen one.
//
// "Not chosen" is the param's own SCHEMA DEFAULT, which is what makes this work
// for both steps without knowing either: Text's default is "plain" and the
// runner's is "default", and neither should put a chip on a card. Comparing
// against a hardcoded list of no-op words would have needed updating every time
// a step joined in.
export function languageOf(
  params: Record<string, unknown> | undefined,
  langParam: string | undefined,
  schemaDefault: unknown,
): string {
  if (!langParam || !params) return "";
  const v = params[langParam];
  if (typeof v !== "string" || v === "") return "";
  return v === schemaDefault ? "" : v;
}
