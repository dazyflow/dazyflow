// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { AlertCircle, ChevronRight, X } from "lucide-react";
import { Button } from "../ui/Button";
import { DropIcon, ICON } from "../../icons";
import { useEscapeToClose } from "../ui/useEscapeToClose";

// ConfigChecklistModal replaces the old "N to configure" popover with a
// proper centered dialog: a click-to-jump checklist of every step still
// missing required values. Each row focuses + frames its node on the
// canvas and closes the modal, so the count stays actionable. ESC and a
// backdrop click both dismiss; it portals to <body> like the app's other
// modals (see ConfirmModal/SettingsModal) so a transformed ancestor in
// the editor tree can't trap the fixed backdrop.
export type ConfigChecklistEntry = {
  nodeID: string;
  label: string;
  messages: { key: string; message: string }[];
  // The step's manifest icon bits, mirrored from NodeCard so the row shows
  // the same glyph/tile as the node on the canvas.
  icon?: {
    name?: string;
    category?: string;
    color?: string;
    brandLogo?: string;
  };
};

// StepIcon adapts this modal's entry shape onto the shared DropIcon, so the
// checklist reads exactly as the canvas does. It used to hand-roll the same
// three-way branch, and had drifted into rendering branded glyphs at ICON.lg
// and tiled ones at ICON.sm — two sizes in one list.
function StepIcon({ icon }: { icon?: ConfigChecklistEntry["icon"] }) {
  return (
    <DropIcon
      icon={icon?.name}
      category={icon?.category}
      brandColor={icon?.color}
      brandLogo={icon?.brandLogo}
      glyphSize={ICON.md}
    />
  );
}

export function ConfigChecklistModal({
  entries,
  onJump,
  onClose,
}: {
  entries: ConfigChecklistEntry[];
  onJump: (nodeID: string) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  useEscapeToClose(onClose);

  return createPortal(
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal config-modal"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="config-modal-title"
      >
        <div className="modal-head config-modal-head">
          <div className="config-modal-heading">
            <span className="config-modal-badge" aria-hidden="true">
              <AlertCircle size={ICON.lg} />
            </span>
            <div>
              <h2 id="config-modal-title">{t("editor.configModalTitle")}</h2>
              <p className="config-modal-lead">
                {t("editor.configModalLead", { count: entries.length })}
              </p>
            </div>
          </div>
          <Button
            variant="ghost"
            size="icon"
            onClick={onClose}
            aria-label={t("settings.close")}
          >
            <X size={ICON.lg} />
          </Button>
        </div>
        <div className="modal-body config-modal-body">
          <ul className="config-checklist">
            {entries.map((entry) => (
              <li key={entry.nodeID}>
                <Button
                  className="config-checklist-item"
                  onClick={() => onJump(entry.nodeID)}
                  aria-label={t("editor.configModalJump", { label: entry.label })}
                >
                  <StepIcon icon={entry.icon} />
                  <span className="config-checklist-text">
                    <span className="config-checklist-name">{entry.label}</span>
                    <ul className="config-checklist-msgs">
                      {entry.messages.map((m) => (
                        <li key={m.key}>{m.message}</li>
                      ))}
                    </ul>
                  </span>
                  <span
                    className="config-checklist-count"
                    aria-hidden="true"
                    title={t("editor.configModalCount", {
                      count: entry.messages.length,
                    })}
                  >
                    {entry.messages.length}
                  </span>
                  <ChevronRight
                    className="config-checklist-arrow"
                    size={ICON.md}
                    aria-hidden="true"
                  />
                </Button>
              </li>
            ))}
          </ul>
        </div>
        <div className="modal-foot">
          {/* Secondary, not primary: the affirmative actions in this dialog are
              the jump-to-drop rows above. A purple Close would draw the eye to
              leaving rather than to the thing the user opened it to fix. */}
          <Button onClick={onClose}>{t("common.close")}</Button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
