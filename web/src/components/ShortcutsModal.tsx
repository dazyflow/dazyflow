import { useEffect } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { X } from "lucide-react";

// ShortcutsModal is the keyboard-shortcuts reference behind the header's
// help button and the "?" key — the discoverability surface pro tools have
// so power users can find the accelerators without hunting. Content is
// static; the mod key adapts to the platform (⌘ on macOS, Ctrl elsewhere).
const isMac =
  typeof navigator !== "undefined" && /mac/i.test(navigator.platform);
const MOD = isMac ? "⌘" : "Ctrl";

type Shortcut = { keys: string[]; descKey: string };

const GENERAL: Shortcut[] = [
  { keys: [MOD, "K"], descKey: "shortcuts.commandBar" },
  { keys: ["?"], descKey: "shortcuts.help" },
];

const EDITOR: Shortcut[] = [
  { keys: [MOD, "K"], descKey: "shortcuts.addStep" },
  { keys: ["Del"], descKey: "shortcuts.deleteSelection" },
  { keys: ["←", "↑", "→", "↓"], descKey: "shortcuts.moveNode" },
];

function Row({ keys, desc }: { keys: string[]; desc: string }) {
  return (
    <div className="shortcut-row">
      <span className="shortcut-keys">
        {keys.map((k, i) => (
          <kbd key={i}>{k}</kbd>
        ))}
      </span>
      <span className="shortcut-desc">{desc}</span>
    </div>
  );
}

export function ShortcutsModal({ onClose }: { onClose: () => void }) {
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
        className="settings-dialog"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label={t("shortcuts.title")}
      >
        <div className="settings-head">
          <h2>{t("shortcuts.title")}</h2>
          <button
            type="button"
            className="icon-button"
            onClick={onClose}
            aria-label={t("common.close")}
          >
            <X size={16} />
          </button>
        </div>
        <div className="settings-body">
          <div className="shortcut-group-label">{t("shortcuts.general")}</div>
          {GENERAL.map((s) => (
            <Row key={s.descKey} keys={s.keys} desc={t(s.descKey)} />
          ))}
          <div className="shortcut-group-label">{t("shortcuts.editor")}</div>
          {EDITOR.map((s) => (
            <Row key={s.descKey} keys={s.keys} desc={t(s.descKey)} />
          ))}
        </div>
      </div>
    </div>,
    document.body,
  );
}
