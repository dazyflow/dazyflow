// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { ExternalLink, RefreshCw } from "lucide-react";
import { isErrorCode } from "../../api";
import { explainApiError } from "../../lib/explainApiError";
import { Button } from "../ui/Button";
import { CopyButton } from "../ui/CopyButton";
import { ICON } from "../../icons";
import { Loading } from "../ui/Loading";
import { Modal } from "../ui/Modal";

// useShareLink + ShareLinkModal: the shared body of the two "public link"
// dialogs (workspace overview → /tv/…, collection → /board/…). They differ in
// which endpoints they call, their copy, and what they warn about; the
// load/create/regenerate/disable lifecycle is identical and lives here.
//
// The displayed/copied URL is built from the browser's own origin rather than
// the server-reported one: behind a dev proxy the daemon's derived base URL
// can be the internal host, but window.location.origin is always the address
// the operator is actually looking at.

export type ShareLinkApi<L> = {
  /** Fetch the current link; a `share_not_found` rejection means "none". */
  get: () => Promise<L>;
  create: () => Promise<L>;
  remove: () => Promise<unknown>;
  /** Path prefix of the public URL, e.g. "/tv/". */
  path: string;
  /** Message shown when create is rejected with `forbidden`. */
  forbiddenMessage: string;
  onChange?: (link: L | null) => void;
  /** False (e.g. signed out) skips every request. */
  enabled: boolean;
};

export function useShareLink<L extends { token: string }>(
  shareApi: ShareLinkApi<L>,
  /** Values that, when changed, re-fetch the link. */
  deps: readonly unknown[],
) {
  const { t } = useTranslation();
  const [link, setLink] = useState<L | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Latest callbacks without re-running the load effect when a caller passes
  // fresh closures each render.
  const apiRef = useRef(shareApi);
  apiRef.current = shareApi;

  const publicURL = useCallback(
    (l: L) => `${window.location.origin}${shareApi.path}${l.token}`,
    [shareApi.path],
  );

  useEffect(() => {
    if (!shareApi.enabled) return;
    let cancelled = false;
    setLoading(true);
    apiRef.current
      .get()
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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shareApi.enabled, ...deps, t]);

  const create = useCallback(async () => {
    if (!apiRef.current.enabled) return;
    setBusy(true);
    setError(null);
    try {
      const l = await apiRef.current.create();
      setLink(l);
      apiRef.current.onChange?.(l);
    } catch (e) {
      setError(
        isErrorCode(e, "forbidden")
          ? apiRef.current.forbiddenMessage
          : explainApiError(e, t),
      );
    } finally {
      setBusy(false);
    }
  }, [t]);

  const disable = useCallback(async () => {
    if (!apiRef.current.enabled) return;
    setBusy(true);
    setError(null);
    try {
      await apiRef.current.remove();
      setLink(null);
      apiRef.current.onChange?.(null);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  }, [t]);

  return { link, loading, busy, error, publicURL, create, disable };
}

export function ShareLinkModal<L extends { token: string }>({
  share,
  title,
  description,
  openIcon,
  labels,
  linkNotice,
  noLinkNotice,
  onClose,
}: {
  share: ReturnType<typeof useShareLink<L>>;
  title: string;
  description: string;
  openIcon: ReactNode;
  labels: { open: string; create: string; regenerate: string; disable: string };
  /** Shown under the link while one exists. */
  linkNotice: ReactNode;
  /** Shown in place of the link while none exists. */
  noLinkNotice: ReactNode;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const { link, loading, busy, error, publicURL, create, disable } = share;

  return (
    <Modal title={title} size="lg" onClose={onClose}>
      <div className="modal-body">
        <p className="settings-help">{description}</p>

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
              <CopyButton text={publicURL(link)} label={t("common.copyLink")} />
              <Button
                onClick={() => window.open(publicURL(link), "_blank", "noreferrer")}
              >
                {openIcon}
                {labels.open}
              </Button>
            </div>
            {linkNotice}
          </>
        ) : (
          noLinkNotice
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
              {labels.disable}
            </Button>
            <Button onClick={create} disabled={busy}>
              <RefreshCw size={ICON.xs} />
              {labels.regenerate}
            </Button>
          </>
        ) : (
          !loading && (
            <Button onClick={create} disabled={busy} variant="primary">
              <ExternalLink size={ICON.xs} />
              {labels.create}
            </Button>
          )
        )}
        <Button onClick={onClose}>{t("common.close")}</Button>
      </div>
    </Modal>
  );
}
