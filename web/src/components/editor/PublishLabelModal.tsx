// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { ReactNode, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { Rocket } from "lucide-react";
import { Button } from "../ui/Button";
import { Callout } from "../ui/Callout";
import { ICON } from "../../icons";
import { useEscapeToClose } from "../ui/useEscapeToClose";

// PublishLabelModal confirms a publish / go-live and nudges the user to give
// the release an optional name. The name field is always offered but never
// required: "Publish without a name" commits with no label, while the primary
// button commits with whatever was typed (an empty value is treated as no
// label and leaves any existing name untouched server-side). Escape and a
// backdrop click cancel without publishing. The input is autofocused so a
// name can be typed straight away.
//
// `warning` + `connect` turn this into a gate rather than a plain confirm:
// when the flow references an app nobody has connected yet, going live would
// arm automatic triggers for a run that cannot succeed, and the user would
// have no reason to look at the run list to find out. The warning names what
// is missing and `connect` becomes the emphasised action, which demotes
// publishing to a deliberate override instead of the default click.
export function PublishLabelModal({
  title,
  message,
  confirmLabel,
  warning,
  connect,
  onPublish,
  onCancel,
}: {
  title: string;
  message: string;
  confirmLabel: string;
  // warning renders above the name field when publishing now would produce a
  // flow that can't run. Omitted (the normal case) renders nothing.
  warning?: ReactNode;
  // connect offers the fix for that warning. When present it takes the
  // primary slot in the footer so "publish anyway" is never the default.
  connect?: { label: string; onClick: () => void };
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

  useEscapeToClose(onCancel);

  // Portal to <body> for the same reason as ConfirmModal: a transformed or
  // container-query ancestor in the editor tree would otherwise trap the
  // position:fixed backdrop inside that subtree.
  return createPortal(
    <div className="modal-backdrop" onClick={onCancel}>
      <div
        className={
          "modal confirm-dialog" + (connect ? " publish-gate" : "")
        }
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
      >
        <div className="modal-head">
          <h2>{title}</h2>
        </div>
        <div className="modal-body">
          {warning && <Callout variant="warning">{warning}</Callout>}
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
        <div className="modal-foot">
          <Button onClick={() => onPublish("")}>
            {t("editor.publishWithoutName")}
          </Button>
          <Button
            variant={connect ? undefined : "primary"}
            onClick={() => onPublish(value.trim())}
          >
            <Rocket size={ICON.sm} />
            {confirmLabel}
          </Button>
          {connect && (
            <Button variant="primary" onClick={connect.onClick}>
              {connect.label}
            </Button>
          )}
        </div>
      </div>
    </div>,
    document.body,
  );
}
