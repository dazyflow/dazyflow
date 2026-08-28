// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { Copy, ExternalLink, RefreshCw, Tv } from "lucide-react";
import { api, isErrorCode } from "../../api";
import { explainApiError } from "../../lib/explainApiError";
import { useAuth } from "../../auth";
import { Button } from "../ui/Button";
import type { ShareLink } from "../../types";
import { ICON } from "../../icons";
import { useEscapeToClose } from "../ui/useEscapeToClose";
import { Loading } from "../ui/Loading";

// ShareOverviewModal manages the workspace's single public overview link —
// the cryptic, login-free TV-dashboard URL. It loads the existing link on
// open, lets the user create one if none exists, copy it, open it, and
// regenerate (which invalidates the old link) or disable it.
//
// The displayed/copied URL is built from the browser's own origin rather than
// the server-reported one: behind a dev proxy the daemon's derived base URL
// can be the internal host, but window.location.origin is always the address
// the operator is actually looking at.
export function ShareOverviewModal({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const { token, activeTenant, activeWorkspace } = useAuth();
  const [link, setLink] = useState<ShareLink | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const tenant = activeTenant || undefined;
  const workspace = activeWorkspace || undefined;

  const publicURL = (l: ShareLink) =>
    `${window.location.origin}/tv/${l.token}`;

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    setLoading(true);
    api
      .getShare(token, tenant, workspace)
      .then((l) => {
        if (!cancelled) setLink(l);
      })
      .catch((e) => {
        if (cancelled) return;
        // No link yet is the expected first-open state, not an error.
        if (!isErrorCode(e, "share_not_found")) {
          setError(explainApiError(e, t));
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [token, tenant, workspace, t]);

  const create = useCallback(async () => {
    if (!token) return;
    setBusy(true);
    setError(null);
    try {
      const l = await api.createShare(token, tenant, workspace);
      setLink(l);
    } catch (e) {
      setError(
        isErrorCode(e, "forbidden")
          ? t("share.forbidden")
          : explainApiError(e, t),
      );
    } finally {
      setBusy(false);
    }
  }, [token, tenant, workspace, t]);

  const disable = useCallback(async () => {
    if (!token) return;
    setBusy(true);
    setError(null);
    try {
      await api.deleteShare(token, tenant, workspace);
      setLink(null);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  }, [token, tenant, workspace, t]);

  const copy = useCallback(async () => {
    if (!link) return;
    try {
      await navigator.clipboard.writeText(publicURL(link));
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      /* clipboard may be blocked; user can select + copy manually */
    }
  }, [link]);

  useEscapeToClose(onClose);

  return createPortal(
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal"
        style={{ maxWidth: 560 }}
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
      >
        <div className="modal-head">
          <h2>{t("share.title")}</h2>
        </div>
        <div className="modal-body">
          <p className="settings-help">{t("share.description")}</p>

          {loading ? (
            <Loading inline />
          ) : link ? (
            <>
              <div className="secret-reveal">{publicURL(link)}</div>
              <div
                style={{
                  display: "flex",
                  gap: "var(--space-2)",
                  flexWrap: "wrap",
                  marginTop: "var(--space-3)",
                }}
              >
                <Button onClick={copy}>
                  <Copy size={ICON.xs} />
                  {copied ? t("share.copied") : t("common.copyLink")}
                </Button>
                <Button
                  onClick={() =>
                    window.open(publicURL(link), "_blank", "noreferrer")
                  }
                >
                  <Tv size={ICON.xs} />
                  {t("share.open")}
                </Button>
              </div>
              <p className="desc" style={{ marginTop: "var(--space-3)" }}>
                {t("share.warning")}
              </p>
            </>
          ) : (
            <p className="desc">{t("share.none")}</p>
          )}

          {error && (
            <p className="desc" style={{ color: "var(--status-failed)" }}>
              {error}
            </p>
          )}
        </div>
        <div className="modal-foot">
          {link ? (
            <>
              <Button onClick={disable} disabled={busy} variant="danger">
                {t("share.disable")}
              </Button>
              <Button onClick={create} disabled={busy}>
                <RefreshCw size={ICON.xs} />
                {t("share.regenerate")}
              </Button>
            </>
          ) : (
            !loading && (
              <Button onClick={create} disabled={busy} variant="primary">
                <ExternalLink size={ICON.xs} />
                {t("share.create")}
              </Button>
            )
          )}
          <Button onClick={onClose}>{t("common.close")}</Button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
