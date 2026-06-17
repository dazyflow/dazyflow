import { useEffect } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { AlertCircle, ChevronRight, X } from "lucide-react";
import { iconFor, isBrandedIcon, dropColor } from "../icons";

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

// StepIcon renders the same icon tile a node shows on the canvas: a brand
// logo image, a self-coloured branded glyph, or a category-tinted gradient
// tile with a lucide glyph. Mirrors the three-way branch in NodeCard so the
// checklist reads as the flow does.
function StepIcon({ icon }: { icon?: ConfigChecklistEntry["icon"] }) {
  if (icon?.brandLogo) {
    return (
      <div className="icon brand-logo">
        <img src={icon.brandLogo} alt="" draggable={false} />
      </div>
    );
  }
  const Glyph = iconFor(icon?.name, icon?.category);
  if (isBrandedIcon(icon?.name)) {
    return (
      <div className="icon branded">
        <Glyph size={18} strokeWidth={2.2} />
      </div>
    );
  }
  const color = dropColor(icon?.category, icon?.color);
  return (
    <div
      className="icon"
      style={{
        background: `linear-gradient(135deg, ${color}, color-mix(in srgb, ${color} 70%, #fff))`,
      }}
    >
      <Glyph size={15} color="#140d30" strokeWidth={2.2} />
    </div>
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
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return createPortal(
    <div className="settings-backdrop" onClick={onClose}>
      <div
        className="settings-dialog config-modal"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="config-modal-title"
      >
        <div className="settings-head config-modal-head">
          <div className="config-modal-heading">
            <span className="config-modal-badge" aria-hidden="true">
              <AlertCircle size={18} />
            </span>
            <div>
              <h2 id="config-modal-title">{t("editor.configModalTitle")}</h2>
              <p className="config-modal-lead">
                {t("editor.configModalLead", { count: entries.length })}
              </p>
            </div>
          </div>
          <button
            className="icon ghost"
            onClick={onClose}
            aria-label={t("settings.close")}
          >
            <X size={18} />
          </button>
        </div>
        <div className="settings-body config-modal-body">
          <ul className="config-checklist">
            {entries.map((entry) => (
              <li key={entry.nodeID}>
                <button
                  type="button"
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
                    size={16}
                    aria-hidden="true"
                  />
                </button>
              </li>
            ))}
          </ul>
        </div>
        <div className="settings-foot">
          <button type="button" className="primary" onClick={onClose}>
            {t("common.close")}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
