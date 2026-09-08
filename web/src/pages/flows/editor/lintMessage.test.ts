// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { lintMessage } from "./lintMessage";
import type { LintIssue } from "../../../types";

const t = (k: string, o?: Record<string, unknown>) =>
  o ? `${k}:${JSON.stringify(o)}` : k;

const issue = (over: Partial<LintIssue>): LintIssue => ({
  code: "script_language_mismatch",
  severity: "warn",
  message: "the English fallback",
  ...over,
});

describe("lintMessage", () => {
  it("quotes both names for a language mismatch", () => {
    const got = lintMessage(
      issue({ values: { language: "python", interpreter: "bash" } }),
      undefined,
      t,
    );
    expect(got).toContain("editor.lintScriptMismatch");
    expect(got).toContain("python");
    expect(got).toContain("bash");
  });

  it("names the language for a script that is not a program", () => {
    const got = lintMessage(
      issue({ code: "script_language_unrunnable", values: { language: "sql" } }),
      undefined,
      t,
    );
    expect(got).toContain("editor.lintScriptUnrunnable");
    expect(got).toContain("sql");
  });

  it("falls back to the daemon's message when the data is missing", () => {
    expect(lintMessage(issue({}), undefined, t)).toBe("the English fallback");
    expect(lintMessage(issue({ values: { language: "python" } }), undefined, t)).toBe(
      "the English fallback",
    );
  });

  it("leaves a code it does not know alone", () => {
    expect(lintMessage(issue({ code: "something_new" }), undefined, t)).toBe(
      "the English fallback",
    );
  });
});
