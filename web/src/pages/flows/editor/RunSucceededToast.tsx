// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { Button } from "../../../components/ui/Button";

// Says so when a run worked. Without this the only success signal was a border
// tint on each node, which reads as "nothing happened" to anyone not looking
// for it — so this carries the last step's output too, answering "what did it
// produce?" where the user is standing.
export function RunSucceededToast({
  run,
  onDismiss,
}: {
  run: { runID: string; label: string; preview: string };
  onDismiss: () => void;
}) {
  const { t } = useTranslation();
  return (
        <div
          role="status"
          className="editor-run-done"
          style={{
            background: "var(--surface)",
            border: "1px solid var(--success)",
            padding: "10px 14px",
            borderRadius: "var(--r-2)",
            fontSize: "var(--text-md)",
            color: "var(--ink)",
            boxShadow: "0 2px 8px color-mix(in srgb, var(--success) 25%, transparent)",
            pointerEvents: "auto",
            display: "flex",
            flexDirection: "column",
            gap: 6,
          }}
        >
          <div
            style={{
              display: "flex",
              alignItems: "flex-start",
              justifyContent: "space-between",
              gap: 8,
            }}
          >
            <strong style={{ color: "var(--success)" }}>
              {run.label
                ? t("editor.runSucceededWith", { label: run.label })
                : t("editor.runSucceeded")}
            </strong>
            <Button
              variant="ghost"
              onClick={() => onDismiss()}
              style={{ fontSize: "var(--text-xs)", padding: "2px 8px" }}
              aria-label={t("common.dismiss")}
            >
              {t("common.dismiss")}
            </Button>
          </div>
          {run.preview && (
            <pre
              style={{
                margin: 0,
                maxHeight: 160,
                overflow: "auto",
                whiteSpace: "pre-wrap",
                wordBreak: "break-word",
                fontSize: "var(--text-sm)",
                color: "var(--muted)",
              }}
            >
              {run.preview}
            </pre>
          )}
          <Link
            to={`/runs/${run.runID}`}
            style={{ fontSize: "var(--text-sm)", alignSelf: "flex-start" }}
          >
            {t("editor.runSucceededDetails")}
          </Link>
        </div>
  );
}
