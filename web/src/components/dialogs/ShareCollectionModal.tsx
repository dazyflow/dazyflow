// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { Copy, ExternalLink, RefreshCw, Table2 } from "lucide-react";
import { api, isErrorCode } from "../../api";
import { explainApiError } from "../../lib/explainApiError";
import { useAuth } from "../../auth";
import { Button } from "../ui/Button";
import { Callout } from "../ui/Callout";
import type { CollectionShareLink } from "../../types";
import { ICON } from "../../icons";
import { useEscapeToClose } from "../ui/useEscapeToClose";
import { Loading } from "../ui/Loading";

// ShareCollectionModal manages one collection's public link — the cryptic,
// login-free URL to a read-only table of its rows.
//
// The warning above the button is not decoration. The workspace-overview link
// publishes a sanitized snapshot, so its dialog can be breezy; this one
// publishes the collection's actual rows to anyone who ends up holding the
// URL, and the person clicking may not have thought about what is in there.
// So the consequence is stated before the link exists, not after.
//
// The displayed URL is built from the browser's own origin rather than the
// server-reported one: behind a dev proxy the daemon's derived base URL can be
// the internal host, while window.location.origin is always the address the
// operator is actually looking at.
export function ShareCollectionModal({
  collection,
  onClose,
  // Told to the caller so the Collections list can mark the collection public
  // (or stop marking it) without refetching the whole list.
  onChange,
}: {
  collection: string;
  onClose: () => void;
  onChange?: (link: CollectionShareLink | null) => void;
}) {
  const { t } = useTranslation();
  const { token, activeTenant, activeWorkspace } = useAuth();
  const [link, setLink] = useState<CollectionShareLink | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const tenant = activeTenant || undefined;
  const workspace = activeWorkspace || undefined;

  const publicURL = (l: CollectionShareLink) =>
    `${window.location.origin}/board/${l.token}`;

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    setLoading(true);
    api
      .getCollectionShare(token, collection, tenant, workspace)
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
  }, [token, collection, tenant, workspace, t]);

  const create = useCallback(async () => {
    if (!token) return;
    setBusy(true);
    setError(null);
    try {
      const l = await api.createCollectionShare(
        token,
        collection,
        tenant,
        workspace,
      );
      setLink(l);
      onChange?.(l);
    } catch (e) {
      setError(
        isErrorCode(e, "forbidden")
          ? t("shareCollection.forbidden")
          : explainApiError(e, t),
      );
    } finally {
      setBusy(false);
    }
  }, [token, collection, tenant, workspace, t, onChange]);

  const disable = useCallback(async () => {
    if (!token) return;
    setBusy(true);
    setError(null);
    try {
      await api.deleteCollectionShare(token, collection, tenant, workspace);
      setLink(null);
      onChange?.(null);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  }, [token, collection, tenant, workspace, t, onChange]);

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
          <h2>{t("shareCollection.title", { name: collection })}</h2>
        </div>
        <div className="modal-body">
          <p className="settings-help">{t("shareCollection.description")}</p>

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
                  {copied ? t("common.copied") : t("common.copyLink")}
                </Button>
                <Button
                  onClick={() =>
                    window.open(publicURL(link), "_blank", "noreferrer")
                  }
                >
                  <Table2 size={ICON.xs} />
                  {t("shareCollection.open")}
                </Button>
              </div>
              <div style={{ marginTop: "var(--space-3)" }}>
                <Callout variant="warning">
                  {t("shareCollection.liveWarning")}
                </Callout>
              </div>
            </>
          ) : (
            <Callout variant="warning">
              {t("shareCollection.beforeYouShare")}
            </Callout>
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
                {t("shareCollection.disable")}
              </Button>
              <Button onClick={create} disabled={busy}>
                <RefreshCw size={ICON.xs} />
                {t("shareCollection.regenerate")}
              </Button>
            </>
          ) : (
            !loading && (
              <Button onClick={create} disabled={busy} variant="primary">
                <ExternalLink size={ICON.xs} />
                {t("shareCollection.create")}
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
