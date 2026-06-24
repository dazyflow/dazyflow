import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { Button } from "./Button";

// PromptModal is the app's themed replacement for window.prompt() — a
// single-line text input in the standard settings-dialog chrome. Used for
// "new folder" and "rename / move". Mount it conditionally; it resolves
// through onSubmit (with the trimmed value) or onCancel. Escape and a
// backdrop click both cancel.
//
// On open the input is focused and its basename is selected (the part after
// the last "/"), so renaming a path like "src/widgets/main.go" pre-selects
// just "main.go" while leaving the folder prefix in place for a move.
export function PromptModal({
  title,
  label,
  hint,
  initialValue,
  confirmLabel,
  onSubmit,
  onCancel,
}: {
  title: string;
  label: string;
  hint?: string;
  initialValue?: string;
  confirmLabel: string;
  onSubmit: (value: string) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const [value, setValue] = useState(initialValue ?? "");
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const el = inputRef.current;
    if (!el) return;
    el.focus();
    const slash = el.value.lastIndexOf("/");
    el.setSelectionRange(slash + 1, el.value.length);
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onCancel();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onCancel]);

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const v = value.trim();
    if (v) onSubmit(v);
  };

  return createPortal(
    <div className="settings-backdrop" onClick={onCancel}>
      <form
        className="settings-dialog"
        onClick={(e) => e.stopPropagation()}
        onSubmit={submit}
        role="dialog"
        aria-modal="true"
      >
        <div className="settings-head">
          <h2>{title}</h2>
        </div>
        <div className="settings-body">
          <div className="sf-field">
            <div className="label-row">
              <label>{label}</label>
            </div>
            <input
              ref={inputRef}
              value={value}
              onChange={(e) => setValue(e.target.value)}
              spellCheck={false}
              autoComplete="off"
            />
            {hint && <p className="desc">{hint}</p>}
          </div>
        </div>
        <div className="settings-foot">
          <Button onClick={onCancel}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" variant="primary" disabled={!value.trim()}>
            {confirmLabel}
          </Button>
        </div>
      </form>
    </div>,
    document.body,
  );
}
