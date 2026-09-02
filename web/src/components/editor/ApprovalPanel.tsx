// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../../api";
import { explainApiError } from "../../lib/explainApiError";
import { useAuth } from "../../auth";
import { Button } from "../ui/Button";

// ApprovalPanel is the full approve/reject control: decision plus an optional
// comment, which rides out the step's `comment` port. It mounts on RUN-SCOPED
// surfaces only. The editor is graph-scoped (its runID is "whichever run is
// latest"), so with two runs parked on the same step it would decide one
// without saying which; the canvas card keeps NodeApproveBar for the run the
// editor is already watching. The panel owns its transient state. Resolving an
// approval flips the node status over SSE, so there is no onResolved callback.
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
          className="muted"
          style={{
            fontSize: "var(--text-md)",
            marginBottom: "var(--space-2)",
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
      <div style={{ display: "flex", gap: "var(--space-2)" }}>
        <Button variant="primary" disabled={!!busy || !token} onClick={resolve("approve")}>
          {busy === "approve" ? t("inspector.approving") : t("common.approve")}
        </Button>
        <Button variant="ghost" disabled={!!busy || !token} onClick={resolve("reject")}>
          {busy === "reject" ? t("inspector.rejecting") : t("inspector.reject")}
        </Button>
      </div>
      {error && (
        <div style={{ color: "var(--danger)", fontSize: "var(--text-sm)", marginTop: "var(--space-1h)" }}>
          {error}
        </div>
      )}
    </div>
  );
}
