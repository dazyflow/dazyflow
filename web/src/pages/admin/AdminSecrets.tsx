// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Secrets } from "./Secrets";
import { AdminSecretManager } from "./AdminSecretManager";

// AdminSecrets is the org's credential home, reached from Admin → Secrets.
// It groups the two closely-related surfaces under one destination using
// the Flow-settings underline tabs (.settings-tabs):
//   - Values:  the tenant ${secret.NAME} vault (Secrets)
//   - Secret manager: bring-your-own Vault/AWS/GCP (AdminSecretManager)
// Org secret values used to live in the Connections hub; they're org-scoped
// config, so they belong with the org settings, next to the external
// secret-manager config rather than alongside the app catalog.
//
// The active tab rides in ?tab= so deep links land right, and the editor's
// "set up this credential" link (/admin/secrets?focus=NAME) reaches the
// Values tab with its ?focus= intact.
export function AdminSecrets() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const tab = params.get("tab") === "manager" ? "manager" : "values";

  const setTab = (next: "values" | "manager") => {
    const p = new URLSearchParams(params);
    p.set("tab", next);
    // `focus` only applies to the Values tab; drop it when leaving so a
    // stale highlight doesn't re-fire after switching back.
    if (next !== "values") p.delete("focus");
    setParams(p, { replace: true });
  };

  return (
    <div className="page">
      <h1>{t("common.secrets")}</h1>
      <p className="page-sub">{t("admin.secrets.intro")}</p>

      <div className="settings-tabs admin-secrets-tabs" role="tablist">
        <button
          type="button"
          role="tab"
          aria-selected={tab === "values"}
          className={tab === "values" ? "active" : ""}
          onClick={() => setTab("values")}
        >
          {t("admin.secrets.tabValues")}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === "manager"}
          className={tab === "manager" ? "active" : ""}
          onClick={() => setTab("manager")}
        >
          {t("admin.secrets.tabManager")}
        </button>
      </div>

      <div className="admin-secrets-body">
        {tab === "values" ? <Secrets /> : <AdminSecretManager />}
      </div>
    </div>
  );
}
