// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslation } from "react-i18next";
import { Button } from "../../../components/ui/Button";
import { oauthProviderDisplay } from "../../../integrationMeta";
import { integrationName } from "../../../lib/dropText";
import type { MissingConnection, SetupNeed } from "../../../lib/requiredConnections";
import { useEscapeToClose } from "../../../components/ui/useEscapeToClose";

// ConnectionGate warns, before a run, that the flow references OAuth
// accounts and/or credentials the tenant hasn't set up. Offers the
// high-leverage next action (go to Connections) plus an escape hatch
// ("Run anyway") because the detection is a heuristic — a node could
// resolve its token/secret another way the editor can't see.
export function ConnectionGate({
  missing,
  missingSecrets,
  missingSetups,
  adminBlockedProviders,
  adminBlockedSecretRefs,
  slackChannels,
  canConnect,
  connectLabel,
  onConnect,
  onRunAnyway,
  onCancel,
}: {
  missing: MissingConnection[];
  missingSecrets: string[];
  // canConnect = hasPerm("secret:write"); when false the user can't connect
  // apps, so the Connect button is replaced with an "ask an admin" note.
  canConnect: boolean;
  // missingSetups names apps with a service connection (API key / endpoint)
  // that isn't configured — the ConnectionFields shape (Claude, ntfy, SMTP).
  missingSetups: SetupNeed[];
  // adminBlockedProviders / adminBlockedSecretRefs name the OAuth
  // providers and ${secret.NAME} refs the graph would need but the
  // operator hasn't enabled on this install. Rendered as a separate,
  // explicitly admin-side section so the user doesn't try to "Connect"
  // something they can't reach. Empty arrays = nothing admin-blocked.
  adminBlockedProviders: string[];
  adminBlockedSecretRefs: string[];
  slackChannels: string[];
  // The primary button's text, resolved by the caller because only it knows
  // where onConnect actually goes — the Apps page, one app's own page, or the
  // org's secret store. A single label ("Go to Connections") named a page this
  // product doesn't have.
  connectLabel: string;
  onConnect: () => void;
  onRunAnyway: () => void;
  onCancel: () => void;
}) {
  const { t, i18n } = useTranslation();
  const hasUserFixable =
    missing.length > 0 || missingSecrets.length > 0 || missingSetups.length > 0;
  const hasAdminBlocked =
    adminBlockedProviders.length > 0 || adminBlockedSecretRefs.length > 0;
  useEscapeToClose(onCancel);

  return (
    <div className="modal-backdrop" onClick={onCancel}>
      <div
        className="modal"
        style={{ maxWidth: 460 }}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
      >
        <div className="modal-head">
          <h2>{t("connGate.title")}</h2>
        </div>
        <div className="modal-body">
          {hasUserFixable && (
            <p className="conn-gate-lede">{t("connGate.lede")}</p>
          )}
          {!hasUserFixable && hasAdminBlocked && (
            <p className="conn-gate-lede">{t("connGate.adminLede")}</p>
          )}
          {(missing.length > 0 || missingSetups.length > 0) && (
            <>
              <div className="conn-gate-section-head">{t("connGate.appsHead")}</div>
              <ul className="conn-gate-list">
                {missing.map((m) => (
                  <li key={`${m.provider}::${m.account}`}>
                    <strong>{oauthProviderDisplay(m.provider).name}</strong>
                    <span className="conn-gate-account">{m.account}</span>
                  </li>
                ))}
                {missingSetups.map((s) => (
                  <li key={s.slug}>
                    <strong>{integrationName(s.integration, i18n.language)}</strong>
                  </li>
                ))}
              </ul>
            </>
          )}
          {missingSecrets.length > 0 && (
            <>
              <div className="conn-gate-section-head">{t("connGate.secretsHead")}</div>
              <ul className="conn-gate-list">
                {missingSecrets.map((name) => (
                  <li key={name}>
                    <code>{name}</code>
                  </li>
                ))}
              </ul>
            </>
          )}
          {hasAdminBlocked && (
            <>
              <div className="conn-gate-section-head conn-gate-admin-head">
                {t("connGate.adminBlockedHead")}
              </div>
              <p className="desc">{t("connGate.adminBlockedBody")}</p>
              <ul className="conn-gate-list conn-gate-admin-list">
                {adminBlockedProviders.map((p) => (
                  <li key={`prov::${p}`}>
                    <strong>{oauthProviderDisplay(p).name}</strong>
                  </li>
                ))}
                {adminBlockedSecretRefs.map((n) => (
                  <li key={`sec::${n}`}>
                    <code>{n}</code>
                  </li>
                ))}
              </ul>
            </>
          )}
          {slackChannels.length > 0 && (
            <div>
              <div className="conn-gate-section-head">{t("connGate.slackHead")}</div>
              <p className="desc">
                {t("connGate.slackBody", { channels: slackChannels.join(", ") })}
              </p>
            </div>
          )}
        </div>
        {!canConnect && hasUserFixable && (
          <p className="desc">{t("connGate.noPermNote")}</p>
        )}
        <div className="modal-foot">
          <Button onClick={onRunAnyway}>
            {t("connGate.runAnyway")}
          </Button>
          {canConnect && (
            <Button variant="primary" onClick={onConnect}>
              {connectLabel}
            </Button>
          )}
        </div>
      </div>
    </div>
  );
}
