// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useMemo, useState } from "react";
import { KeyRound, Plus, Search, ShieldOff, Trash2 } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../../auth";
import { api, APIError } from "../../api";
import type { APIKeySummary, IssuedAPIKey } from "../../types";
import { Button } from "../../components/ui/Button";
import { IssueKeyModal } from "../../components/dialogs/IssueKeyModal";
import { RevealSecretModal } from "../../components/dialogs/RevealSecretModal";
import { formatDate } from "../../lib/datetime";
import { explainApiError } from "../../lib/explainApiError";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { ICON } from "../../icons";
import { Loading } from "../../components/ui/Loading";
import { Notice } from "../../components/ui/Notice";
import { EmptyState } from "../../components/ui/EmptyState";

export function AdminAPIKeys() {
  const { t } = useTranslation();
  const { token, hasPerm, activeTenant } = useAuth();
  const [keys, setKeys] = useState<APIKeySummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [filter, setFilter] = useState("");
  // confirmRevoke holds the key id the operator is being asked about;
  // null = no dialog. Inline (no modal portal) so the focus stays on
  // the row they're acting on.
  const [confirmRevoke, setConfirmRevoke] = useState<string | null>(null);
  // Holds the just-minted key so the UI can show the secret once.
  // Clearing it (close) is one-way; the secret is never recoverable.
  const [revealed, setRevealed] = useState<IssuedAPIKey | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.listAPIKeys(token, activeTenant || undefined);
      setKeys(r.keys ?? []);
      setError(null);
    } catch (e) {
      const err = e as APIError | Error;
      if (err instanceof APIError && err.status === 501) {
        setError(t("admin.apiKeys.notConfigured"));
      } else {
        setError(explainApiError(err, t));
      }
    } finally {
      setLoading(false);
    }
  }, [token, activeTenant, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // Pre-compute the filtered view so the table + the empty-after-filter
  // copy can both read from one source.
  const filtered = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return keys;
    return keys.filter(
      (k) =>
        k.id.toLowerCase().includes(q) ||
        k.subject.toLowerCase().includes(q) ||
        k.roles.some((r) => r.name.toLowerCase().includes(q)),
    );
  }, [keys, filter]);

  if (!hasPerm("organization:admin")) {
    return (
      <ErrorNotice>
        <Trans i18nKey="admin.apiKeys.needAdmin" components={[<code />]} />
      </ErrorNotice>
    );
  }

  const revoke = async (id: string) => {
    if (!token) return;
    try {
      await api.revokeAPIKey(token, id);
      setConfirmRevoke(null);
      await refresh();
    } catch (e) {
      setError(explainApiError(e, t));
    }
  };

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <KeyRound size={ICON.xl} />
            {t("admin.apiKeys.title")}
          </h1>
          <div className="sub">{t("admin.apiKeys.subtitle")}</div>
        </div>
        {keys.length > 0 && (
          <Button variant="primary" onClick={() => setCreating(true)}>
            <Plus size={ICON.sm} />
            {t("admin.apiKeys.issueKey")}
          </Button>
        )}
      </div>

      {error && (
        <ErrorNotice style={{ marginBottom: "var(--space-4)" }}>
{error}
        </ErrorNotice>
      )}

      {!loading && !error && keys.length === 0 && (
        <EmptyState
          icon={KeyRound}
          title={t("admin.apiKeys.emptyTitle")}
          action={
            <Button variant="primary" onClick={() => setCreating(true)}>
              <Plus size={ICON.sm} />
              {t("admin.apiKeys.issueFirst")}
            </Button>
          }
        >
          {t("admin.apiKeys.emptyBody")}
        </EmptyState>
      )}

      {loading && keys.length === 0 && (
        <Loading />
      )}

      {keys.length > 0 && (
        <>
          <div className="api-key-toolbar">
            <div className="api-key-search">
              <Search size={ICON.sm} aria-hidden="true" />
              <input
                type="search"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder={t("admin.apiKeys.searchPlaceholder")}
                aria-label={t("admin.apiKeys.searchPlaceholder")}
              />
            </div>
            <div className="api-key-count">
              {filter
                ? t("admin.apiKeys.filteredCount", {
                    shown: filtered.length,
                    total: keys.length,
                  })
                : t("admin.apiKeys.totalCount", { total: keys.length })}
            </div>
          </div>

          {filtered.length === 0 ? (
            <Notice>
              {t("admin.apiKeys.filterNoMatch", { query: filter })}
            </Notice>
          ) : (
            <div className="card" style={{ padding: 0, overflow: "hidden" }}>
              {/* Six columns behind the card's overflow:hidden: on a phone the
                  expiry, the status and the revoke button were clipped away
                  entirely, so the table scrolls in here instead. */}
              <div className="run-table-scroll">
                <table className="run-table">
                  <thead>
                    <tr>
                      <th>{t("admin.apiKeys.colId")}</th>
                      <th>{t("admin.apiKeys.colSubject")}</th>
                      <th>{t("admin.apiKeys.colRoles")}</th>
                      <th>{t("admin.apiKeys.colExpires")}</th>
                      <th>{t("common.status")}</th>
                      <th></th>
                    </tr>
                  </thead>
                  <tbody>
                    {filtered.map((k) => (
                      <APIKeyRow
                        key={k.id}
                        k={k}
                        confirming={confirmRevoke === k.id}
                        onConfirm={() => setConfirmRevoke(k.id)}
                        onCancelConfirm={() => setConfirmRevoke(null)}
                        onRevoke={() => revoke(k.id)}
                      />
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}
        </>
      )}

      {creating && (
        <IssueKeyModal
          onCancel={() => setCreating(false)}
          onIssued={(issued) => {
            setCreating(false);
            setRevealed(issued);
            void refresh();
          }}
          onError={(msg) => setError(msg)}
        />
      )}

      {revealed && (
        <RevealSecretModal
          issued={revealed}
          onClose={() => setRevealed(null)}
        />
      )}
    </div>
  );
}

function APIKeyRow({
  k,
  confirming,
  onConfirm,
  onCancelConfirm,
  onRevoke,
}: {
  k: APIKeySummary;
  confirming: boolean;
  onConfirm: () => void;
  onCancelConfirm: () => void;
  onRevoke: () => void;
}) {
  const { t } = useTranslation();
  const expiresLabel = formatExpires(t, k.expires_at);
  return (
    <tr className={confirming ? "row-confirming" : undefined}>
      <td style={{ fontFamily: "var(--font-mono)", fontSize: "var(--text-sm)" }}>{k.id}</td>
      <td>{k.subject}</td>
      <td style={{ fontSize: "var(--text-sm)" }}>
        {k.roles.map((r) => r.name).join(", ")}
      </td>
      <td style={{ fontSize: "var(--text-sm)", color: expiresLabel.tone }}>
        {expiresLabel.text}
      </td>
      <td>
        <StatusBadge status={k.status} />
      </td>
      <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
        {k.status === "active" &&
          (confirming ? (
            <span className="inline-confirm">
              {t("admin.apiKeys.revokeReally")}{" "}
              <Button variant="danger" onClick={onRevoke}>
                {t("common.revoke")}
              </Button>
              <Button variant="ghost" onClick={onCancelConfirm}>
                {t("common.cancel")}
              </Button>
            </span>
          ) : (
            <Button
              variant="ghost"
              onClick={onConfirm}
              title={t("admin.apiKeys.revokeTitle")}
            >
              <Trash2 size={ICON.sm} />
            </Button>
          ))}
        {k.status === "revoked" && (
          <span className="muted" style={{ fontSize: "var(--text-sm)" }}>
            <ShieldOff className="icon-lede" size={ICON.xs} />
            {t("admin.apiKeys.revokedAlready")}
          </span>
        )}
      </td>
    </tr>
  );
}

function StatusBadge({ status }: { status: APIKeySummary["status"] }) {
  const { t } = useTranslation();
  // Map status to a label + tone class; CSS picks the colour from
  // .key-status.<status>.
  const label =
    status === "active"
      ? t("admin.apiKeys.statusActive")
      : status === "expired"
        ? t("admin.apiKeys.statusExpired")
        : t("admin.apiKeys.statusRevoked");
  return <span className={`key-status ${status}`}>{label}</span>;
}

// formatExpires turns the optional ISO timestamp into either an
// absolute date + how-soon hint, or "never" with a muted tone. The
// tone helps the operator spot keys that have already expired or are
// about to.
function formatExpires(
  t: (key: string, opts?: Record<string, unknown>) => string,
  iso: string | null | undefined,
): { text: string; tone: string } {
  if (!iso) {
    return { text: t("admin.apiKeys.expiresNever"), tone: "var(--muted)" };
  }
  const when = new Date(iso);
  const now = Date.now();
  const days = Math.round((when.getTime() - now) / (24 * 60 * 60 * 1000));
  const dateStr = formatDate(when);
  if (days < 0) {
    return {
      text: t("admin.apiKeys.expiredOn", { date: dateStr }),
      tone: "var(--danger)",
    };
  }
  if (days <= 7) {
    return {
      text: t("admin.apiKeys.expiresSoon", { date: dateStr, days }),
      tone: "var(--warning)",
    };
  }
  return { text: dateStr, tone: "var(--ink)" };
}
