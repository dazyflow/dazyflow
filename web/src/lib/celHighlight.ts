// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// A tiny, dependency-free CEL syntax highlighter. It tokenizes a formula and
// returns HTML where each token is wrapped in a <span class="cel-…">, for the
// overlay-behind-textarea editor (CelInput). It is NOT a parser — it never
// judges validity (that's the server linter's job); it only colors tokens so
// a formula is easier to read while typing.
//
// Safety: every run of source text is HTML-escaped before it's emitted, and
// the only markup added is our own fixed <span> tags — so user input can't
// inject HTML even though the result is set via innerHTML.

// Known built-in functions and macros — colored as functions even without a
// following "(" (the macros read like keywords). Mirrors the CEL surface the
// Expression drop exposes.
const BUILTINS = new Set([
  "has", "size", "int", "uint", "double", "string", "bool", "bytes",
  "timestamp", "duration", "type", "dyn", "matches", "contains",
  "startsWith", "endsWith", "map", "filter", "all", "exists", "exists_one",
]);

// Language keywords / literals.
const KEYWORDS = new Set(["true", "false", "null", "in"]);

// The variables the Expression env binds — highlighted so they stand out from
// the fields the user reaches through them.
const VARS = new Set(["input", "now"]);

function escapeHTML(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

function span(cls: string, text: string): string {
  return `<span class="cel-${cls}">${escapeHTML(text)}</span>`;
}

// One master scanner. Order matters: strings and numbers must win over
// identifiers/operators. Group 1 whitespace, 2 string, 3 number, 4 identifier,
// 5 operator, 6 punctuation.
const TOKEN =
  /(\s+)|((?:r|b)?"(?:[^"\\]|\\.)*"|(?:r|b)?'(?:[^'\\]|\\.)*')|(\b0x[0-9a-fA-F]+\b|\b\d[\d_]*(?:\.\d+)?(?:[eE][+-]?\d+)?[uU]?\b)|([A-Za-z_][A-Za-z0-9_]*)|(&&|\|\||==|!=|<=|>=|[-+*/%!<>?:.=])|([()[\]{},])/g;

function classifyIdent(name: string, after: string): string {
  if (KEYWORDS.has(name)) return "kw";
  if (VARS.has(name)) return "var";
  if (BUILTINS.has(name)) return "fn";
  // A bare identifier immediately followed by "(" is a function call.
  if (/^\s*\(/.test(after)) return "fn";
  return "ident";
}

export function highlightCEL(src: string): string {
  let out = "";
  let last = 0;
  TOKEN.lastIndex = 0;
  let m: RegExpExecArray | null;
  while ((m = TOKEN.exec(src)) !== null) {
    // Any gap the scanner skipped (shouldn't happen, but stay lossless).
    if (m.index > last) out += escapeHTML(src.slice(last, m.index));
    if (m[1] !== undefined) out += escapeHTML(m[1]); // whitespace, verbatim
    else if (m[2] !== undefined) out += span("str", m[2]);
    else if (m[3] !== undefined) out += span("num", m[3]);
    else if (m[4] !== undefined) out += span(classifyIdent(m[4], src.slice(TOKEN.lastIndex)), m[4]);
    else if (m[5] !== undefined) out += span("op", m[5]);
    else if (m[6] !== undefined) out += span("punct", m[6]);
    last = TOKEN.lastIndex;
  }
  if (last < src.length) out += escapeHTML(src.slice(last));
  return out;
}
