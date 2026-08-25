// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// The highlighter sits UNDER the caret of a textarea, which makes one property
// non-negotiable and easy to break: every character of the input must come back
// exactly once. Lose one and the whole highlight layer shifts, so the colours
// stop lining up with the text from that point on. Most of these tests are
// really about that, with the token classification checked alongside.
import { describe, expect, it } from "vitest";
import { scriptLangFor, tokenizeScript, type ScriptLang } from "./scriptHighlight";

const flat = (src: string, lang: ScriptLang) =>
  tokenizeScript(src, lang)
    .map((t) => (typeof t === "string" ? t : t.text))
    .join("");

const kinds = (src: string, lang: ScriptLang) =>
  tokenizeScript(src, lang).flatMap((t) => (typeof t === "string" ? [] : [[t.kind, t.text]]));

describe("tokenizeScript", () => {
  it("is lossless for every language", () => {
    const samples: [ScriptLang, string][] = [
      ["shell", "#!/bin/sh\nfor f in *.csv; do\n  echo \"$f\" | tr a-z A-Z\ndone\n"],
      ["python", 'import sys\n\ndef go(n=3):\n    """docs"""\n    return [i for i in range(n)]\n'],
      ["powershell", "<# header #>\nparam($Name)\nif ($Name -eq 'x') { Write-Output 1 }\n"],
      ["js", "// go\nconst x = `a${1}b`; /* done */\n"],
      // Half-typed code is the normal state of a box someone is typing in.
      ["shell", 'echo "unterminated'],
      ["python", "s = '''open"],
      ["js", "/* never closed"],
      ["shell", "$"],
      ["shell", ""],
    ];
    for (const [lang, src] of samples) {
      expect(flat(src, lang), `${lang}: ${JSON.stringify(src)}`).toBe(src);
    }
  });

  it("colours shell comments, strings, keywords and variables", () => {
    const got = kinds('if [ -f "$f" ]; then # check\n', "shell");
    expect(got).toEqual([
      ["keyword", "if"],
      ["string", '"$f"'],
      ["keyword", "then"],
      ["comment", "# check"],
    ]);
  });

  it("does not read a keyword out of the middle of a word", () => {
    // "iffy" is not `if` + "fy", and "done_at" is not `done` + "_at".
    expect(kinds("iffy done_at", "shell")).toEqual([]);
  });

  it("keeps a shell single-quoted string literal, backslashes and all", () => {
    // Single quotes take no escapes in a shell, so a trailing backslash must
    // not swallow the closing quote and colour the rest of the script.
    expect(kinds("echo 'a\\' end", "shell")).toEqual([
      ["keyword", "echo"],
      ["string", "'a\\'"],
    ]);
  });

  it("stops a single-line string at the end of its line", () => {
    // One stray apostrophe should not tint everything after it.
    const got = kinds("echo 'oops\nls -l\n", "shell");
    expect(got).toEqual([
      ["keyword", "echo"],
      ["string", "'oops"],
    ]);
  });

  it("reads a Python triple-quoted string across lines", () => {
    expect(kinds('"""one\ntwo"""', "python")).toEqual([["string", '"""one\ntwo"""']]);
  });

  it("marks a ${…} reference in every language, not just the ones with variables", () => {
    // These are substituted by the server before the machine sees the script,
    // so they are not the language they sit in — and that is the point of
    // showing them differently.
    for (const lang of ["shell", "python", "js", "powershell"] as ScriptLang[]) {
      expect(kinds("x = ${secret.KEY}", lang)).toContainEqual(["var", "${secret.KEY}"]);
    }
  });

  it("does not mistake part of an identifier for a number", () => {
    expect(kinds("utf8", "python")).toEqual([]);
    expect(kinds("timeout = 30", "python")).toEqual([["number", "30"]]);
  });

  it("reads a PowerShell block comment", () => {
    expect(kinds("<#\nnotes\n#>\nparam($x)", "powershell")).toEqual([
      ["comment", "<#\nnotes\n#>"],
      ["keyword", "param"],
      ["var", "$x"],
    ]);
  });
});

describe("scriptLangFor", () => {
  it("maps the step's shell param onto a highlighter", () => {
    expect(scriptLangFor("python")).toBe("python");
    expect(scriptLangFor("powershell")).toBe("powershell");
    expect(scriptLangFor("node")).toBe("js");
    expect(scriptLangFor("bash")).toBe("shell");
    // The machine's own shell IS a shell, and so is an unset or unknown value —
    // which is what a step carries before anyone touches the field.
    expect(scriptLangFor("default")).toBe("shell");
    expect(scriptLangFor(undefined)).toBe("shell");
    expect(scriptLangFor("erlang")).toBe("shell");
  });
});
