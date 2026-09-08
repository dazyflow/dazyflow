// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { Button } from "./Button";
import { useEscapeToClose } from "./useEscapeToClose";

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

  useEscapeToClose(onCancel);

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const v = value.trim();
    if (v) onSubmit(v);
  };

  return createPortal(
    <div className="modal-backdrop" onClick={onCancel}>
      <form
        className="modal"
        onClick={(e) => e.stopPropagation()}
        onSubmit={submit}
        role="dialog"
        aria-modal="true"
      >
        <div className="modal-head">
          <h2>{title}</h2>
        </div>
        <div className="modal-body">
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
        <div className="modal-foot">
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
