// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { lintMessage } from "./lintMessage";
import type { LintIssue } from "../../../types";

// The editor builds its own sentence for the codes it knows and falls back to
// the daemon's English `message` for the rest. The two script-language findings
// were the first to need DATA rather than a field name, so what these pin is
// that the data path works — and that the fallback still catches a finding
// arriving without it, from an older daemon or a hand-made API call.
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
    // Same contract the field-based codes have: no data, no invented sentence.
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
