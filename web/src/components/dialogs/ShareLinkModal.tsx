// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Copy, ExternalLink, RefreshCw } from "lucide-react";
import { isErrorCode } from "../../api";
import { explainApiError } from "../../lib/explainApiError";
import { FEEDBACK } from "../../lib/timing";
import { useAuth } from "../../auth";
import { Button } from "../ui/Button";
import { Modal } from "../ui/Modal";
import { Loading } from "../ui/Loading";
import { ICON } from "../../icons";

// The shared machinery behind the public-link dialogs (workspace overview TV
// board, collection board). Both load the existing link on open, let the user
// create one if none exists, copy or open it, and regenerate (which
// invalidates the old link) or disable it. They differ only in which endpoint
// they call, the path the link lives under, and the words around it.
//
// The displayed/copied URL is built from the browser's own origin rather than
// the server-reported one: behind a dev proxy the daemon's derived base URL
// can be the internal host, but window.location.origin is always the address
// the operator is actually looking at.

type Scope = [token: string, tenant: string | undefined, workspace: string | undefined];

export type ShareLinkOps<L> = {
  get: (...s: Scope) => Promise<L>;
  create: (...s: Scope) => Promise<L>;
  remove: (...s: Scope) => Promise<unknown>;
};

export function useShareLink<L extends { token: string }>({
  ops,
  scopeKey,
  basePath,
  forbiddenMessage,
  onChange,
}: {
  /** API calls; read through a ref, so an inline object is fine. */
  ops: ShareLinkOps<L>;
  /** Changes when the thing being shared changes (e.g. collection name). */
  scopeKey: string;
  /** Path under the origin the token is appended to, e.g. "/tv". */
  basePath: string;
  forbiddenMessage: string;
  onChange?: (link: L | null) => void;
}) {
  const { t } = useTranslation();
  const { token, activeTenant, activeWorkspace } = useAuth();
  const [link, setLink] = useState<L | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const tenant = activeTenant || undefined;
  const workspace = activeWorkspace || undefined;

  const opsRef = useRef(ops);
  opsRef.current = ops;
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  const forbiddenRef = useRef(forbiddenMessage);
  forbiddenRef.current = forbiddenMessage;

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    setLoading(true);
    opsRef.current
      .get(token, tenant, workspace)
      .then((l) => {
        if (!cancelled) setLink(l);
      })
      .catch((e) => {
        if (cancelled) return;
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
  }, [token, tenant, workspace, scopeKey, t]);

  const create = useCallback(async () => {
    if (!token) return;
    setBusy(true);
    setError(null);
    try {
      const l = await opsRef.current.create(token, tenant, workspace);
      setLink(l);
      onChangeRef.current?.(l);
    } catch (e) {
      setError(
        isErrorCode(e, "forbidden")
          ? forbiddenRef.current
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
      await opsRef.current.remove(token, tenant, workspace);
      setLink(null);
      onChangeRef.current?.(null);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  }, [token, tenant, workspace, t]);

  const url = link ? `${window.location.origin}${basePath}/${link.token}` : null;

  const copy = useCallback(async () => {
    if (!url) return;
    try {
      await navigator.clipboard.writeText(url);
      setCopied(true);
      window.setTimeout(() => setCopied(false), FEEDBACK.copied);
    } catch {
      /* clipboard may be blocked; user can select + copy manually */
    }
  }, [url]);

  return { link, url, loading, busy, error, copied, create, disable, copy };
}

export function ShareLinkModal<L extends { token: string }>({
  title,
  description,
  openLabel,
  openIcon,
  createLabel,
  regenerateLabel,
  disableLabel,
  linkNotice,
  emptyNotice,
  onClose,
  ...hook
}: Parameters<typeof useShareLink<L>>[0] & {
  title: string;
  description: string;
  openLabel: string;
  openIcon: ReactNode;
  createLabel: string;
  regenerateLabel: string;
  disableLabel: string;
  /** Shown under the link once one exists. */
  linkNotice: ReactNode;
  /** Shown in place of the link while none exists. */
  emptyNotice: ReactNode;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const { url, loading, busy, error, copied, create, disable, copy } =
    useShareLink(hook);

  return (
    <Modal title={title} size="md" onClose={onClose}>
      <div className="modal-head">
        <h2>{title}</h2>
      </div>
      <div className="modal-body">
        <p className="settings-help">{description}</p>

        {loading ? (
          <Loading inline />
        ) : url ? (
          <>
            <div className="secret-reveal">{url}</div>
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
              <Button onClick={() => window.open(url, "_blank", "noreferrer")}>
                {openIcon}
                {openLabel}
              </Button>
            </div>
            <div style={{ marginTop: "var(--space-3)" }}>{linkNotice}</div>
          </>
        ) : (
          emptyNotice
        )}

        {error && (
          <p className="desc" style={{ color: "var(--status-failed)" }}>
            {error}
          </p>
        )}
      </div>
      <div className="modal-foot">
        {url ? (
          <>
            <Button onClick={disable} disabled={busy} variant="danger">
              {disableLabel}
            </Button>
            <Button onClick={create} disabled={busy}>
              <RefreshCw size={ICON.xs} />
              {regenerateLabel}
            </Button>
          </>
        ) : (
          !loading && (
            <Button onClick={create} disabled={busy} variant="primary">
              <ExternalLink size={ICON.xs} />
              {createLabel}
            </Button>
          )
        )}
        <Button onClick={onClose}>{t("common.close")}</Button>
      </div>
    </Modal>
  );
}
