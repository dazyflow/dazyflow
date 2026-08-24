// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslation } from "react-i18next";
import { Send, X } from "lucide-react";
import { Button } from "../../../components/ui/Button";
import { ICON } from "../../../icons";
import { useEscapeToClose } from "../../../components/ui/useEscapeToClose";

// The "Send test event" dialog: an editable JSON sample the editor POSTs to
// /test-trigger, so a webhook flow can be fired from the canvas without an
// external caller. The sample itself is built by ./testEventSample.
export function TestEventDialog({
  json,
  error,
  canRun,
  onChange,
  onSubmit,
  onClose,
}: {
  json: string;
  // Parse failure from the last submit attempt; keeps the dialog open with the
  // message inline rather than firing a malformed payload.
  error: string | null;
  canRun: boolean;
  onChange: (json: string) => void;
  onSubmit: () => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  useEscapeToClose(onClose);

  return (
        <div className="modal-backdrop" onClick={() => onClose()}>
          <div
            className="modal"
            onClick={(e) => e.stopPropagation()}
            aria-modal="true"
            role="dialog"
            aria-label={t("editor.testRunHeading")}
          >
            <div className="modal-head">
              <strong>
                <Send className="icon-lede" size={ICON.sm} />
                {t("editor.testRunHeading")}
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
              <p className="sub" style={{ marginTop: 0 }}>
                {t("editor.testRunHelp")}
              </p>
              <textarea
                className="test-sample-input"
                value={json}
                spellCheck={false}
                onChange={(e) => onChange(e.target.value)}
                rows={12}
              />
              {error && (
                <div style={{ color: "var(--danger)", fontSize: "var(--text-sm)", marginTop: "var(--space-1h)" }}>
                  {t("editor.testRunBadJSON", { error: error })}
                </div>
              )}
            </div>
            <div className="modal-foot">
              <Button variant="ghost" onClick={() => onClose()}>
                {t("common.dismiss")}
              </Button>
              <Button
                variant="primary"
                onClick={() => onSubmit()}
                disabled={!canRun}
              >
                <Send size={ICON.sm} />
                {t("editor.testRunFire")}
              </Button>
            </div>
          </div>
        </div>
  );
}
