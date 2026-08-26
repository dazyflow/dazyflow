// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { LintIssue, Manifest } from "../../../types";
import { humanize } from "../../../components/fields/SchemaForm";

// Turns a lint finding from the daemon into the sentence the editor's warning
// banner shows. Pure string work with no tie to the editor's state, so it reads
// better here than five thousand lines into a component.

// lintFieldLabel names a flagged param the way the Inspector form does: by its
// schema `title`, falling back to the humanized key — never the raw slug. Env
// vars have no schema entry and are shown by their bare name in the Inspector,
// so we keep that.
function lintFieldLabel(path: string, manifest: Manifest | undefined): string {
  if (path.startsWith("env.")) return path.slice(4);
  const top = path.split(/[.[]/)[0];
  const title = manifest?.params_schema?.properties?.[top]?.title;
  return title && title.length > 0 ? title : humanize(top);
}

// lintMessage builds the user-facing sentence for a lint finding using
// Inspector-style field labels and no node/module/field slugs. The node itself
// is already highlighted on the canvas, so it goes unnamed. Codes we don't have
// a label-based string for (or findings missing field data) fall back to the
// backend `message`, which keeps the slug-bearing phrasing for CLI/API readers.
export function lintMessage(
  issue: LintIssue,
  manifest: Manifest | undefined,
  t: (key: string, opts?: Record<string, unknown>) => string,
): string {
  const fields = (issue.fields ?? []).map((f) => lintFieldLabel(f, manifest));
  const field = fields.join(", ");
  switch (issue.code) {
    case "template_placeholder":
      if (field) return t("editor.lintPlaceholder", { field });
      break;
    case "hardcoded_secret":
      if (field) return t("editor.lintHardcoded", { field });
      break;
    case "dangling_reference":
      if (field) return t("editor.lintDangling", { field });
      break;
    case "secret_to_persistence":
      return t("editor.lintSecretPersist");
    // These two quote two names rather than a field, so they read from `values`
    // — and fall through to the English `message` if either is missing, which
    // is the same contract the field-based cases above have.
    case "script_language_mismatch":
      if (issue.values?.language && issue.values?.interpreter) {
        return t("editor.lintScriptMismatch", {
          language: issue.values.language,
          interpreter: issue.values.interpreter,
        });
      }
      break;
    case "script_language_unrunnable":
      if (issue.values?.language) {
        return t("editor.lintScriptUnrunnable", { language: issue.values.language });
      }
      break;
  }
  return issue.message;
}
