// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Trans, useTranslation } from "react-i18next";
import { AlertCircle, PlugZap } from "lucide-react";
import { Button } from "../../components/ui/Button";
import { AuthLayout } from "../../components/auth/AuthLayout";
import { Loading } from "../../components/ui/Loading";
import { useAuth } from "../../auth";
import { api } from "../../api";
import { explainApiError } from "../../lib/explainApiError";
import type { MCPAuthorizationRequest } from "../../types";
import { ICON } from "../../icons";

// AuthorizeMCP is the OAuth consent page an MCP client (claude.ai, the Claude
// apps, Claude Code) sends the user to via /oauth/authorize. Approving hands
// the browser back to the client with a one-time code; the client then acts
// as this user in this workspace until the connection is removed in Settings.
export function AuthorizeMCP() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [params] = useSearchParams();
  const requestID = params.get("request") ?? "";
  const [details, setDetails] = useState<MCPAuthorizationRequest | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!token || !requestID) return;
    api
      .getMCPAuthorization(token, requestID)
      .then(setDetails)
      .catch((e) => setError(explainApiError(e, t)));
  }, [token, requestID, t]);

  const decide = async (approve: boolean) => {
    if (!token) return;
    setBusy(true);
    setError(null);
    try {
      const { redirect_url } = await api.decideMCPAuthorization(token, requestID, approve);
      window.location.assign(redirect_url);
    } catch (e) {
      setError(explainApiError(e, t));
      setBusy(false);
    }
  };

  return (
    <AuthLayout>
      <div className="signin invite-card">
        <div className="invite-card-icon">
          <PlugZap size={28} />
        </div>
        <h1>{t("authorizeMcp.title")}</h1>
        {!requestID && <p>{t("authorizeMcp.missingRequest")}</p>}
        {requestID && !details && !error && <Loading inline />}
        {error && (
          <div className="error">
            <AlertCircle className="icon-inline" size={ICON.sm} /> {error}
          </div>
        )}
        {details && (
          <>
            <p>
              <Trans
                i18nKey="authorizeMcp.body"
                values={{
                  client: details.client_name,
                  account: details.account,
                  workspace: details.workspace,
                }}
                components={[<strong />, <strong />, <code />]}
              />
            </p>
            <p className="muted">
              <Trans
                i18nKey="authorizeMcp.returnTo"
                values={{ host: details.redirect_host }}
                components={[<strong />]}
              />
            </p>
            <div className="invite-cta-row">
              <Button variant="primary" onClick={() => void decide(true)} disabled={busy}>
                {t("authorizeMcp.approve")}
              </Button>
              <Button variant="secondary" onClick={() => void decide(false)} disabled={busy}>
                {t("authorizeMcp.deny")}
              </Button>
            </div>
          </>
        )}
      </div>
    </AuthLayout>
  );
}
