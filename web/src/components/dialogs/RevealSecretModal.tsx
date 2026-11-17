// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslation } from "react-i18next";
import { Button } from "../ui/Button";
import type { IssuedAPIKey } from "../../types";
import { Modal } from "../ui/Modal";
import { CopyButton } from "../ui/CopyButton";

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

  return (
    <Modal title={t("revealSecret.title")} onClose={onClose} size="md">
      <div className="modal-head">
        <h2>{t("revealSecret.title")}</h2>
      </div>
      <div className="modal-body">
        <p className="settings-help">
          {t("revealSecret.warning")}
        </p>
        <div className="secret-reveal">{issued.secret}</div>
        <CopyButton
          text={issued.secret}
          label={t("revealSecret.copy")}
          style={{ marginTop: "var(--space-3)" }}
        />
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
      <div className="modal-foot">
        <Button variant="primary" onClick={onClose}>
          {t("revealSecret.done")}
        </Button>
      </div>
    </Modal>
  );
}
