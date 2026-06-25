import { useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { useAuth } from "../auth";
import { Button } from "./Button";

// ApprovalPanel is the inline approve/reject control shown in the Inspector
// when the selected node is an await_approval step parked in "awaiting". It
// was extracted from Inspector so the inspector body stays focused on param
// editing; the panel owns its own transient state (the comment + in-flight
// flag) which is discarded when the user clicks away (the panel unmounts).
//
// Resolving an approval flips the node status over SSE and dispatches any
// downstream nodes, so there's no onResolved callback — the canvas updates
// itself.
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
      setError(explainApiError(e, t));
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
