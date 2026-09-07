// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { ChevronDown, ChevronUp } from "lucide-react";
import { Button } from "../../../components/ui/Button";
import { ICON } from "../../../icons";

// Says so when a run worked. Without this the only success signal was a border
// tint on each node, which reads as "nothing happened" to anyone not looking
// for it.
//
// It floats over the canvas (click-through, like .editor-trigger-hint) rather
// than sitting in the docked banner strip, which it shared with everything
// else: the strip grows to 40vh, so announcing a success took a bite out of the
// flow its author was looking at.
//
// And what the last step produced is FOLDED AWAY. It used to be printed
// straight into the strip — `JSON.stringify(…, null, 2)` of whatever the leaf
// port held, up to a 160px scroll box. For the audience this product is for,
// that is not an answer to "did it work?", it is a wall of syntax over the top
// of their flow; the ones who do want it are a click away from it, and the
// people who do this for a living have "See the full run" as well.
export function RunSucceededToast({
  run,
  onDismiss,
}: {
  run: { runID: string; label: string; preview: string };
  onDismiss: () => void;
}) {
  const { t } = useTranslation();
  const [showOutput, setShowOutput] = useState(false);

  return (
    <div role="status" className="editor-run-done">
      <div className="editor-run-done-line">
        <strong className="editor-run-done-head">
          {run.label
            ? t("editor.runSucceededWith", { label: run.label })
            : t("editor.runSucceeded")}
        </strong>
        {run.preview && (
          <Button
            variant="ghost"
            size="sm"
            className="editor-run-done-toggle"
            aria-expanded={showOutput}
            onClick={() => setShowOutput((v) => !v)}
          >
            {showOutput ? (
              <ChevronUp size={ICON.sm} aria-hidden="true" />
            ) : (
              <ChevronDown size={ICON.sm} aria-hidden="true" />
            )}
            {t(showOutput ? "editor.runSucceededHide" : "editor.runSucceededShow")}
          </Button>
        )}
        <Link className="editor-run-done-link" to={`/runs/${run.runID}`}>
          {t("editor.runSucceededDetails")}
        </Link>
        <Button
          variant="ghost"
          size="sm"
          className="editor-run-done-x"
          onClick={() => onDismiss()}
          aria-label={t("common.dismiss")}
        >
          {t("common.dismiss")}
        </Button>
      </div>
      {showOutput && run.preview && (
        <pre className="editor-run-done-output">{run.preview}</pre>
      )}
    </div>
  );
}
