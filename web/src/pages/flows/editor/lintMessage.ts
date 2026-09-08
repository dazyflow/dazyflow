// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { LintIssue, Manifest } from "../../../types";
import { humanize } from "../../../components/fields/SchemaForm";


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
