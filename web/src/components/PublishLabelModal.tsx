// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { Rocket } from "lucide-react";
import { Button } from "./Button";

// PublishLabelModal confirms a publish / go-live and nudges the user to give
// the release an optional name. The name field is always offered but never
// required: "Publish without a name" commits with no label, while the primary
// button commits with whatever was typed (an empty value is treated as no
// label and leaves any existing name untouched server-side). Escape and a
// backdrop click cancel without publishing. The input is autofocused so a
// name can be typed straight away.
export function PublishLabelModal({
  title,
  message,
  confirmLabel,
  onPublish,
  onCancel,
}: {
  title: string;
  message: string;
  confirmLabel: string;
  // onPublish receives the trimmed label (empty string = publish unnamed).
  onPublish: (label: string) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const [value, setValue] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onCancel();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onCancel]);

  // Portal to <body> for the same reason as ConfirmModal: a transformed or
  // container-query ancestor in the editor tree would otherwise trap the
  // position:fixed backdrop inside that subtree.
  return createPortal(
    <div className="settings-backdrop" onClick={onCancel}>
      <div
        className="settings-dialog confirm-dialog"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
      >
        <div className="settings-head">
          <h2>{title}</h2>
        </div>
        <div className="settings-body">
          <label className="publish-label-field">
            <span>{t("editor.publishLabelField")}</span>
            <input
              ref={inputRef}
              type="text"
              value={value}
              placeholder={t("editor.publishLabelPlaceholder")}
              onChange={(e) => setValue(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") onPublish(value.trim());
              }}
            />
          </label>
          <p className="confirm-message">{message}</p>
        </div>
        <div className="settings-foot">
          <Button onClick={() => onPublish("")}>
            {t("editor.publishWithoutName")}
          </Button>
          <Button variant="primary" onClick={() => onPublish(value.trim())}>
            <Rocket size={14} style={{ marginRight: 5 }} />
            {confirmLabel}
          </Button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
