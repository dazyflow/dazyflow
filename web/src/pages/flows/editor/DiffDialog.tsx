// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslation } from "react-i18next";
import { GitCompare, Rocket, X } from "lucide-react";
import { Button } from "../../../components/ui/Button";
import { ICON } from "../../../icons";
import { diffIsEmpty, type GraphDiff } from "../../../lib/diffGraphs";

// What the draft changes relative to the published revision. Execution-focused
// (nodes, edges, params, meta) — diffGraphs filters cosmetic moves out, so an
// empty list here means "nothing that would run differently".
export function DiffDialog({
  diff,
  loading,
  publishing,
  canPublish,
  onPublish,
  onClose,
}: {
  diff: GraphDiff | null;
  loading: boolean;
  publishing: boolean;
  // False while previewing an older revision — publishing the draft from under
  // a preview would push something the user isn't looking at.
  canPublish: boolean;
  onPublish: () => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  return (
        <div className="modal-backdrop" onClick={() => onClose()}>
          <div
            className="modal diff-modal"
            onClick={(e) => e.stopPropagation()}
            role="dialog"
            aria-label={t("editor.diffTitle")}
          >
            <div className="modal-head">
              <strong>
                <GitCompare size={ICON.sm} style={{ verticalAlign: -2, marginRight: 6 }} />
                {t("editor.diffHeading")}
              </strong>
              <Button
                size="icon"
                onClick={() => onClose()}
                aria-label={t("common.dismiss")}
              >
                <X size={ICON.md} />
              </Button>
            </div>
            <div className="modal-body">
              {loading ? (
                <div className="history-empty">{t("common.loading")}</div>
              ) : !diff || diffIsEmpty(diff) ? (
                <div className="history-empty">{t("editor.diffNone")}</div>
              ) : (
                <ul className="diff-list">
                  {diff.addedNodes.map((id) => (
                    <li key={`an-${id}`} className="diff-row added">
                      + {t("editor.diffNodeAdded", { id })}
                    </li>
                  ))}
                  {diff.removedNodes.map((id) => (
                    <li key={`rn-${id}`} className="diff-row removed">
                      − {t("editor.diffNodeRemoved", { id })}
                    </li>
                  ))}
                  {diff.changedNodes.map((c) => (
                    <li key={`cn-${c.id}`} className="diff-row changed">
                      ~ {t("editor.diffNodeChanged", { id: c.id, fields: c.fields.join(", ") })}
                    </li>
                  ))}
                  {diff.addedEdges.map((k) => (
                    <li key={`ae-${k}`} className="diff-row added">
                      + {t("editor.diffEdgeAdded")}: <code>{k}</code>
                    </li>
                  ))}
                  {diff.removedEdges.map((k) => (
                    <li key={`re-${k}`} className="diff-row removed">
                      − {t("editor.diffEdgeRemoved")}: <code>{k}</code>
                    </li>
                  ))}
                  {diff.metaChanged.length > 0 && (
                    <li className="diff-row changed">
                      ~ {t("editor.diffMeta", { fields: diff.metaChanged.join(", ") })}
                    </li>
                  )}
                </ul>
              )}
            </div>
            <div className="modal-foot">
              <Button variant="ghost" onClick={() => onClose()}>
                {t("common.dismiss")}
              </Button>
              <Button
                className="editor-publish"
                onClick={() => {
                  onClose();
                  onPublish();
                }}
                disabled={publishing || !canPublish}
              >
                <Rocket size={ICON.sm} style={{ marginRight: 5 }} />
                {t("editor.publish")}
              </Button>
            </div>
          </div>
        </div>
  );
}
