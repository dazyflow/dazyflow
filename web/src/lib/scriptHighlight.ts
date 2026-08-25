// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// A tiny, dependency-free highlighter for the scripts a flow sends to one of
// the org's own machines (the Run on your machine step). One scanner,
// parameterised per language, emitting tokens the editor renders as <span>s.
//
// It is NOT a parser and never judges validity. A script is written for a
// machine this browser cannot see, running an interpreter it cannot inspect, so
// there is nothing here to validate against — the value is purely that a wall
// of monospace text becomes readable while typing: comments recede, strings and
// keywords separate, and a ${secret.…} reference is visibly not shell syntax.
//
// The language set matches the step's "Run it with" dropdown, because that is
// the only thing that says what the text in the box IS. A shell script and a
// Python one share a comment character and nothing else, so guessing from the
// content would colour half of either one wrongly.

// ScriptLang is what the highlighter is told to read the text as.
export type ScriptLang = "shell" | "python" | "powershell" | "js";

// scriptLangFor maps the step's `shell` param onto a highlighter. Unknown (and
// the machine's own shell) reads as POSIX shell, which is what that default
// actually runs.
export function scriptLangFor(shell: string | undefined): ScriptLang {
  switch (shell) {
    case "python":
      return "python";
    case "powershell":
      return "powershell";
    case "node":
      return "js";
    default:
      return "shell";
  }
}

// TokenKind names the classes the editor styles. Deliberately few: five colours
// is what a reader can tell apart at a glance, and a scheme with twelve is a
// scheme nobody reads.
export type TokenKind = "comment" | "string" | "keyword" | "number" | "var";

export type ScriptToken = { kind: TokenKind; text: string } | string;

type LangSpec = {
  // Line comments, as the literal prefix. Every language here has exactly one.
  lineComment: string;
  // Block comments, when the language has them — as [open, close].
  blockComment?: [string, string];
  // The quote characters that open a string, and whether a backslash escapes
  // inside them. Shell single quotes take no escapes at all, which is the one
  // place getting this wrong is visible: '\' would otherwise swallow the quote.
  quotes: { q: string; escapes: boolean; multiline?: boolean }[];
  keywords: Set<string>;
  // A sigil that starts a variable — $ in shell and PowerShell. Python and
  // JavaScript have none, but a ${…} Dazyflow reference still gets marked
  // there, because it is not part of the language and should not read as if it
  // were (see VAR_REF).
  sigil?: string;
};

// Keyword lists are the words that carry a script's shape, not exhaustive
// language vocabularies. A longer list colours more of the text and separates
// less of it.
const LANGS: Record<ScriptLang, LangSpec> = {
  shell: {
    lineComment: "#",
    quotes: [
      { q: '"', escapes: true },
      { q: "'", escapes: false },
    ],
    keywords: new Set([
      "if", "then", "elif", "else", "fi", "for", "while", "until", "do", "done",
      "case", "esac", "in", "function", "select", "time", "return", "exit",
      "break", "continue", "local", "export", "readonly", "set", "unset",
      "shift", "trap", "source", "echo", "cd", "test",
    ]),
    sigil: "$",
  },
  python: {
    lineComment: "#",
    quotes: [
      { q: '"""', escapes: true, multiline: true },
      { q: "'''", escapes: true, multiline: true },
      { q: '"', escapes: true },
      { q: "'", escapes: true },
    ],
    keywords: new Set([
      "and", "as", "assert", "async", "await", "break", "class", "continue",
      "def", "del", "elif", "else", "except", "finally", "for", "from",
      "global", "if", "import", "in", "is", "lambda", "match", "None",
      "nonlocal", "not", "or", "pass", "raise", "return", "True", "False",
      "try", "while", "with", "yield",
    ]),
  },
  powershell: {
    lineComment: "#",
    blockComment: ["<#", "#>"],
    quotes: [
      { q: '"', escapes: true },
      { q: "'", escapes: false },
    ],
    keywords: new Set([
      "begin", "break", "catch", "continue", "data", "do", "dynamicparam",
      "else", "elseif", "end", "exit", "filter", "finally", "for", "foreach",
      "function", "if", "in", "param", "process", "return", "switch", "throw",
      "trap", "try", "until", "while", "class", "enum", "using",
    ]),
    sigil: "$",
  },
  js: {
    lineComment: "//",
    blockComment: ["/*", "*/"],
    quotes: [
      { q: '"', escapes: true },
      { q: "'", escapes: true },
      { q: "`", escapes: true, multiline: true },
    ],
    keywords: new Set([
      "async", "await", "break", "case", "catch", "class", "const", "continue",
      "default", "delete", "do", "else", "export", "extends", "false",
      "finally", "for", "from", "function", "if", "import", "in", "instanceof",
      "let", "new", "null", "of", "return", "super", "switch", "this", "throw",
      "true", "try", "typeof", "undefined", "var", "void", "while", "yield",
    ]),
  },
};

// A Dazyflow reference — ${secret.STRIPE_KEY}, ${node.out} — which the daemon
// substitutes before the script ever reaches the machine. Marked in every
// language, including the two that have no variables of their own: it is the
// one piece of the text that is not the language it is written in, and the one
// worth spotting when a script does not behave.
const VAR_REF = /^\$\{[^}]*\}?/;

const IDENT = /^[A-Za-z_][A-Za-z0-9_]*/;
const NUMBER = /^(?:0[xX][0-9a-fA-F]+|\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/;
// A PowerShell/shell variable after the sigil: a name, or a braced expression.
const SIGIL_NAME = /^(?:\{[^}]*\}?|[A-Za-z_][A-Za-z0-9_]*|[0-9?@*#$!-])/;

// tokenizeScript splits `src` into coloured tokens and the plain runs between
// them. Total: every character of the input comes back exactly once, so the
// highlight layer stays the same length as the text it sits under — the
// property the overlay editor depends on for the caret to line up.
//
// Tolerant on purpose: an unterminated string or comment (which is most of what
// half-typed code is) simply runs to the end of the input rather than
// abandoning the rest of the file uncoloured.
export function tokenizeScript(src: string, lang: ScriptLang): ScriptToken[] {
  const spec = LANGS[lang];
  const out: ScriptToken[] = [];
  let plain = "";
  let i = 0;

  const flush = () => {
    if (plain) out.push(plain);
    plain = "";
  };
  const push = (kind: TokenKind, text: string) => {
    flush();
    out.push({ kind, text });
    i += text.length;
  };

  while (i < src.length) {
    const rest = src.slice(i);

    // A Dazyflow reference wins over a shell variable of the same shape: both
    // start "${", and the reference is the more specific reading.
    const ref = VAR_REF.exec(rest);
    if (ref) {
      push("var", ref[0]);
      continue;
    }

    if (spec.blockComment && rest.startsWith(spec.blockComment[0])) {
      const end = src.indexOf(spec.blockComment[1], i + spec.blockComment[0].length);
      push("comment", end === -1 ? rest : src.slice(i, end + spec.blockComment[1].length));
      continue;
    }

    if (rest.startsWith(spec.lineComment)) {
      const nl = src.indexOf("\n", i);
      push("comment", nl === -1 ? rest : src.slice(i, nl));
      continue;
    }

    // Longest quote first (Python's """ before "), which LANGS orders.
    const quote = spec.quotes.find((q) => rest.startsWith(q.q));
    if (quote) {
      push("string", readString(src, i, quote));
      continue;
    }

    if (spec.sigil && rest.startsWith(spec.sigil)) {
      const name = SIGIL_NAME.exec(rest.slice(spec.sigil.length));
      // A lone sigil is not a variable — in shell it is a literal, and in
      // PowerShell it is a typo. Either way, leave it plain.
      if (name) {
        push("var", spec.sigil + name[0]);
        continue;
      }
    }

    const num = NUMBER.exec(rest);
    // Guarded on the preceding character so the 8 in `utf8` or `$1` is part of
    // the word, not a number sitting inside it.
    if (num && !/[A-Za-z0-9_]/.test(src[i - 1] ?? "")) {
      push("number", num[0]);
      continue;
    }

    const ident = IDENT.exec(rest);
    if (ident) {
      if (spec.keywords.has(ident[0])) {
        push("keyword", ident[0]);
      } else {
        // Consumed as one run so the next iteration cannot re-read the tail of
        // an identifier as a keyword ("iffy" is not `if` + "fy").
        plain += ident[0];
        i += ident[0].length;
      }
      continue;
    }

    plain += src[i];
    i += 1;
  }
  flush();
  return out;
}

// readString returns the whole literal starting at `from`, including its
// quotes, stopping at the end of the line for a single-line quote so one stray
// apostrophe does not colour the rest of the script.
function readString(
  src: string,
  from: number,
  quote: { q: string; escapes: boolean; multiline?: boolean },
): string {
  const q = quote.q;
  let i = from + q.length;
  while (i < src.length) {
    if (quote.escapes && src[i] === "\\") {
      i += 2;
      continue;
    }
    if (!quote.multiline && src[i] === "\n") return src.slice(from, i);
    if (src.startsWith(q, i)) return src.slice(from, i + q.length);
    i += 1;
  }
  return src.slice(from);
}
