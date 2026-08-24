// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../../api";
import { explainApiError } from "../../lib/explainApiError";
import { useAuth } from "../../auth";
import { Button } from "../ui/Button";

// ApprovalPanel is the full approve/reject control — decision plus an optional
// comment, which rides out the step's `comment` port for downstream steps to
// read. It mounts on RUN-SCOPED surfaces only (the run page today; the
// Approvals inbox rolls its own list-shaped variant of the same thing).
//
// It used to mount in the Inspector too, and no longer does. The editor is
// graph-scoped: its runID is "whichever run is latest" (Inspector's
// currentRunID), so with two runs of one flow parked on the same step — which
// nothing prevents, SubmitGraph has no active-run gate — it would decide one
// of them without saying which. A parked run also blocks saving the flow, so
// the inspector's actual job is disabled exactly when the panel appeared. The
// canvas card keeps a quick decide-in-place bar (NodeApproveBar) for the run
// the editor is already watching; anything more deliberate belongs here.
//
// The panel owns its own transient state (the comment + in-flight flag),
// discarded on unmount. Resolving an approval flips the node status over SSE
// and dispatches any downstream nodes, so there's no onResolved callback — the
// view updates itself.
export function ApprovalPanel({
  runID,
  nodeID,
  prompt,
}: {
  runID: string;
  nodeID: string;
  prompt?: string;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [comment, setComment] = useState("");
  const [busy, setBusy] = useState<"approve" | "reject" | null>(null);
  const [error, setError] = useState<string | null>(null);

  const resolve = (decision: "approve" | "reject") => async () => {
    if (!token) return;
    setBusy(decision);
    setError(null);
    try {
      await api.approveNode(token, runID, nodeID, decision, comment || undefined);
      setComment("");
      // SSE delivers the status flip + downstream dispatch; no local refresh.
    } catch (e) {
      setError(explainApiError(e, t, "approval"));
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="inspector-section approve-inline">
      <h4>{t("inspector.awaitingApproval")}</h4>
      {prompt && (
        <div
          style={{
            fontSize: "var(--text-md)",
            color: "var(--muted)",
            marginBottom: 8,
            whiteSpace: "pre-wrap",
          }}
        >
          {prompt}
        </div>
      )}
      <div className="sf-field">
        <div className="label-row">
          <label>{t("inspector.commentOptional")}</label>
        </div>
        <textarea
          rows={2}
          value={comment}
          onChange={(e) => setComment(e.target.value)}
          disabled={!!busy}
          style={{ resize: "vertical" }}
        />
      </div>
      <div style={{ display: "flex", gap: 8 }}>
        <Button variant="primary" disabled={!!busy || !token} onClick={resolve("approve")}>
          {busy === "approve" ? t("inspector.approving") : t("inspector.approve")}
        </Button>
        <Button variant="ghost" disabled={!!busy || !token} onClick={resolve("reject")}>
          {busy === "reject" ? t("inspector.rejecting") : t("inspector.reject")}
        </Button>
      </div>
      {error && (
        <div style={{ color: "var(--danger)", fontSize: "var(--text-sm)", marginTop: 6 }}>
          {error}
        </div>
      )}
    </div>
  );
}
