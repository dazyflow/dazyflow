// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import { Copy } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "../ui/Button";
import type { IssuedAPIKey } from "../../types";
import { ICON } from "../../icons";
import { FEEDBACK } from "../../lib/timing";

// RevealSecretModal renders the one-time view of a freshly-minted API
// key's secret. Once closed, the secret is gone — Dazyflow keeps only
// a salted hash. Used by both the API keys page and the Users page.
export function RevealSecretModal({
  issued,
  onClose,
}: {
  issued: IssuedAPIKey;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(issued.secret);
      setCopied(true);
      window.setTimeout(() => setCopied(false), FEEDBACK.copied);
    } catch {
      /* clipboard may be blocked; user can select + copy manually */
    }
  };
  return (
    <div className="settings-backdrop" onClick={onClose}>
      <div
        className="settings-dialog"
        style={{ maxWidth: 540 }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="settings-head">
          <h2>{t("revealSecret.title")}</h2>
        </div>
        <div className="settings-body">
          <p className="settings-help">
            {t("revealSecret.warning")}
          </p>
          <div className="secret-reveal">{issued.secret}</div>
          <Button onClick={copy} style={{ marginTop: "var(--space-3)" }}>
            <Copy size={ICON.xs} style={{ marginRight: 6 }} />
            {copied ? t("common.copied") : t("revealSecret.copy")}
          </Button>
          <div className="sf-field" style={{ marginTop: "var(--space-4)" }}>
            <div className="label-row">
              <label>{t("revealSecret.keyIdLabel")}</label>
            </div>
            <input
              value={issued.id}
              disabled
              style={{ fontFamily: "var(--font-mono)" }}
            />
          </div>
        </div>
        <div className="settings-foot">
          <Button variant="primary" onClick={onClose}>
            {t("revealSecret.done")}
          </Button>
        </div>
      </div>
    </div>
  );
}
