// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { CheckCircle2, AlertCircle } from "lucide-react";
import { useAuth } from "../../auth";
import { api } from "../../api";
import { highlightCEL } from "../../lib/celHighlight";
import { ICON } from "../../icons";

// CelInput is the formula editor for the Expression drop: a textarea with a
// live CEL syntax-highlight overlay behind it, plus a debounced server-side
// linter that shows the first compile error inline (or a quiet "valid" tick).
//
// The highlight is a <pre> painted UNDER a transparent-text textarea; both
// share identical typography and wrapping so the colored tokens sit exactly
// under the real caret. The textarea owns editing and the caret; the <pre> is
// aria-hidden and never interactive.
export function CelInput({
  value,
  onChange,
}: {
  value: string;
  onChange: (v: string) => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const preRef = useRef<HTMLPreElement>(null);
  const [issue, setIssue] = useState<{ message: string; line: number; column: number } | null>(null);
  const [checking, setChecking] = useState(false);
  const seq = useRef(0);

  // Debounced lint: compile the formula server-side ~350ms after typing stops.
  useEffect(() => {
    if (!token || value.trim() === "") {
      setIssue(null);
      setChecking(false);
      return;
    }
    const id = ++seq.current;
    setChecking(true);
    const handle = setTimeout(() => {
      api
        .validateExpression(token, value)
        .then((r) => {
          if (id !== seq.current) return; // superseded
          setIssue(r.valid ? null : (r.issue ?? { message: "invalid formula", line: 0, column: 0 }));
        })
        .catch(() => id === seq.current && setIssue(null))
        .finally(() => id === seq.current && setChecking(false));
    }, 350);
    return () => clearTimeout(handle);
  }, [token, value]);

  // Keep the highlight scrolled in lockstep with the textarea.
  const syncScroll = (e: React.UIEvent<HTMLTextAreaElement>) => {
    const pre = preRef.current;
    if (!pre) return;
    pre.scrollTop = e.currentTarget.scrollTop;
    pre.scrollLeft = e.currentTarget.scrollLeft;
  };

  const valid = value.trim() !== "" && !checking && !issue;

  return (
    <div className="cel-input">
      <div className="cel-editor">
        <pre
          ref={preRef}
          className="cel-highlight"
          aria-hidden="true"
          // Trailing newline keeps the last line visible when it ends in \n.
          dangerouslySetInnerHTML={{ __html: highlightCEL(value) + "\n" }}
        />
        <textarea
          className="cel-textarea nodrag nowheel"
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          onScroll={syncScroll}
          rows={3}
          placeholder={t("celInput.placeholder")}
        />
      </div>
      {issue ? (
        <div className="cel-issue" role="alert">
          <AlertCircle size={ICON.sm} />
          <span>
            {issue.line > 0
              ? t("celInput.errorAt", { line: issue.line, col: issue.column, message: issue.message })
              : issue.message}
          </span>
        </div>
      ) : valid ? (
        <div className="cel-ok">
          <CheckCircle2 size={ICON.sm} />
          <span>{t("celInput.valid")}</span>
        </div>
      ) : null}
    </div>
  );
}
