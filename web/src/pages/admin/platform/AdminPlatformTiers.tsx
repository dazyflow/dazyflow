// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { Layers, Pencil, Plus, Trash2, X } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../../../auth";
import { api, type PlatformTier } from "../../../api";
import { explainApiError } from "../../../lib/explainApiError";
import { Button } from "../../../components/ui/Button";
import { ConfirmModal } from "../../../components/ui/ConfirmModal";
import { ErrorNotice } from "../../../components/ui/ErrorNotice";
import { ICON } from "../../../icons";
import { slugify } from "../../../lib/format";
import { useEscapeToClose } from "../../../components/ui/useEscapeToClose";
import { Loading } from "../../../components/ui/Loading";

// AdminPlatformTiers manages the reusable limit bundles a platform admin
// assigns to orgs. Built-in Free/Pro can be edited (their limits) but not
// deleted; custom tiers (e.g. Enterprise) are full CRUD. "0" on any limit
// means "inherit the deployment default" — shown as "—".
export function AdminPlatformTiers() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const [tiers, setTiers] = useState<PlatformTier[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState<PlatformTier | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<PlatformTier | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.platformListTiers(token);
      setTiers(r.tiers ?? []);
      setError(null);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setLoading(false);
    }
  }, [token, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  if (!hasPerm("platform:admin")) {
    return (
      <ErrorNotice>
        <Trans i18nKey="admin.platform.needPlatformAdmin" components={[<code />]} />
      </ErrorNotice>
    );
  }

  const blankTier = (): PlatformTier => ({
    id: "",
    name: "",
    plan: "free",
    runs_per_month: 0,
    disk_quota_bytes: 0,
    max_graph_nodes: 0,
    max_flows: 0,
    max_timeout_seconds: 0,
    retention_days: 0,
    max_concurrency: 0,
    max_members: 0,
    polling_allowed: null, // inherit the global default, like the 0-valued limits
    built_in: false,
  });

  const del = async (tier: PlatformTier) => {
    if (!token) return;
    try {
      await api.platformDeleteTier(token, tier.id);
      await refresh();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setConfirmDelete(null);
    }
  };


  return (
    <div>
      <div className="page-title">
        <div className="pa-detail-head">
          <Layers size={ICON.xl} />
          <div>
            <h1>{t("admin.platformTiers.title")}</h1>
            <div className="sub">{t("admin.platformTiers.subtitle")}</div>
          </div>
        </div>
        <Button variant="primary" onClick={() => setEditing(blankTier())}>
          <Plus size={ICON.sm} />
          {t("admin.platformTiers.new")}
        </Button>
      </div>

      {error && (
        <ErrorNotice style={{ marginBottom: "var(--space-3)" }}>
{error}
        </ErrorNotice>
      )}

      {loading ? (
        <Loading />
      ) : (
        <div className="user-list">
          {tiers.map((tier) => (
            <div className="user-card" key={tier.id}>
              <div style={{ minWidth: 0 }}>
                <div className="subject">
                  {tier.name}
                  <span
                    className={"count-pill" + (tier.plan === "pro" ? " active" : "")}
                    style={{ marginLeft: "var(--space-2)" }}
                  >
                    {tier.plan}
                  </span>
                  {tier.built_in && (
                    <span className="count-pill" style={{ marginLeft: "var(--space-2)" }}>
                      {t("admin.platformTiers.builtIn")}
                    </span>
                  )}
                </div>
                <div className="meta">{limitSummary(tier, (k) => t(k))}</div>
              </div>
              <div className="user-card-actions" style={{ flexDirection: "row" }}>
                <Button onClick={() => setEditing(tier)} title={t("common.edit", "Edit")}>
                  <Pencil size={ICON.sm} />
                </Button>
                {!tier.built_in && (
                  <Button variant="danger" onClick={() => setConfirmDelete(tier)}>
                    <Trash2 size={ICON.sm} />
                  </Button>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      {editing && (
        <TierEditor
          tier={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            void refresh();
          }}
          onError={(m) => setError(m)}
        />
      )}
      {confirmDelete && (
        <ConfirmModal
          title={t("admin.platformTiers.deleteTitle", { name: confirmDelete.name })}
          message={t("admin.platformTiers.deleteWarning")}
          confirmLabel={t("common.delete")}
          danger
          onConfirm={() => void del(confirmDelete)}
          onCancel={() => setConfirmDelete(null)}
        />
      )}
    </div>
  );
}

// limitSummary renders a one-line digest of a tier's limits, with "—" for
// inherit-default (0).
function limitSummary(tier: PlatformTier, t: (k: string) => string): string {
  const n = (v: number) => (v > 0 ? String(v) : "—");
  const mb = tier.disk_quota_bytes > 0 ? `${Math.round(tier.disk_quota_bytes / (1024 * 1024))} MB` : "—";
  return [
    `${t("admin.platformTiers.runs")}: ${n(tier.runs_per_month)}`,
    `${t("common.members")}: ${n(tier.max_members)}`,
    `${t("admin.platformTiers.concurrency")}: ${n(tier.max_concurrency)}`,
    `${t("admin.platformTiers.retention")}: ${tier.retention_days > 0 ? `${tier.retention_days}d` : "—"}`,
    `${t("admin.platformTiers.nodes")}: ${n(tier.max_graph_nodes)}`,
    `${t("common.flows")}: ${n(tier.max_flows)}`,
    `${t("admin.platformTiers.disk")}: ${mb}`,
    `${t("admin.platformTiers.polling")}: ${
      tier.polling_allowed == null
        ? "—"
        : tier.polling_allowed
          ? t("common.yes")
          : t("common.no")
    }`,
  ].join(" · ");
}

function TierEditor({
  tier,
  onClose,
  onSaved,
  onError,
}: {
  tier: PlatformTier;
  onClose: () => void;
  onSaved: () => void;
  onError: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const isNew = tier.id === "" && !tier.built_in;
  const [draft, setDraft] = useState<PlatformTier>({ ...tier });
  // Disk is edited in MB for sanity; converted to bytes on save.
  const [diskMB, setDiskMB] = useState<number>(
    tier.disk_quota_bytes > 0 ? Math.round(tier.disk_quota_bytes / (1024 * 1024)) : 0,
  );
  const [busy, setBusy] = useState(false);

  const save = async () => {
    if (!token) return;
    const id = isNew ? slugify(draft.name) : draft.id;
    if (!id) {
      onError(t("admin.platformTiers.nameRequired"));
      return;
    }
    setBusy(true);
    try {
      await api.platformSaveTier(token, {
        ...draft,
        id,
        disk_quota_bytes: Math.max(0, Math.round(diskMB)) * 1024 * 1024,
      });
      onSaved();
    } catch (e) {
      onError(explainApiError(e, t));
      setBusy(false);
    }
  };

  const num = (key: keyof PlatformTier) => (
    <input
      type="number"
      min={0}
      value={(draft[key] as number) ?? 0}
      onChange={(e) => setDraft({ ...draft, [key]: Math.max(0, Number(e.target.value) || 0) })}
    />
  );


  useEscapeToClose(() => !busy && onClose());
  return createPortal(
    <div className="modal-backdrop" onClick={() => !busy && onClose()}>
      <div className="modal" onClick={(e) => e.stopPropagation()} role="dialog" aria-modal="true">
        <div className="modal-head">
          <h2>{isNew ? t("admin.platformTiers.new") : t("admin.platformTiers.edit", { name: tier.name })}</h2>
          <Button variant="ghost" size="icon" onClick={onClose} disabled={busy}>
            <X size={ICON.lg} />
          </Button>
        </div>
        <div className="modal-body pa-tier-form">
          <label>
            {t("common.name")}
            <input
              type="text"
              value={draft.name}
              autoFocus
              onChange={(e) => setDraft({ ...draft, name: e.target.value })}
            />
          </label>
          <label>
            {t("admin.platformTiers.plan")}
            <select value={draft.plan} onChange={(e) => setDraft({ ...draft, plan: e.target.value })}>
              {/* The value is the plan's own name (what the API stores); the
                  label is the word the rest of the product shows for it. */}
              <option value="free">{t("usage.planValueFree")}</option>
              <option value="pro">{t("usage.planValuePro")}</option>
            </select>
          </label>
          <label>
            {t("admin.platformTiers.runs")} <span className="pa-subtext">({t("admin.platformTiers.zeroInherit")})</span>
            {num("runs_per_month")}
          </label>
          <label>
            {t("common.members")} <span className="pa-subtext">({t("admin.platformTiers.zeroInherit")})</span>
            {num("max_members")}
          </label>
          <label>
            {t("admin.platformTiers.concurrency")} <span className="pa-subtext">({t("admin.platformTiers.zeroInherit")})</span>
            {num("max_concurrency")}
          </label>
          <label>
            {t("admin.platformTiers.retention")} <span className="pa-subtext">({t("admin.platformTiers.zeroInherit")})</span>
            {num("retention_days")}
          </label>
          <label>
            {t("admin.platformTiers.nodes")} <span className="pa-subtext">({t("admin.platformTiers.zeroInherit")})</span>
            {num("max_graph_nodes")}
          </label>
          <label>
            {t("common.flows")} <span className="pa-subtext">({t("admin.platformTiers.zeroInherit")})</span>
            {num("max_flows")}
          </label>
          <label>
            {t("admin.platformTiers.timeout")} <span className="pa-subtext">({t("admin.platformTiers.zeroInherit")})</span>
            {num("max_timeout_seconds")}
          </label>
          <label>
            {t("admin.platformTiers.diskMB")} <span className="pa-subtext">({t("admin.platformTiers.zeroInherit")})</span>
            <input
              type="number"
              min={0}
              value={diskMB}
              onChange={(e) => setDiskMB(Math.max(0, Number(e.target.value) || 0))}
            />
          </label>
          <label>
            {t("admin.platformTiers.pollingAllowed")}{" "}
            <span className="pa-subtext">({t("admin.platformTiers.zeroInherit")})</span>
            <select
              value={
                draft.polling_allowed == null
                  ? "default"
                  : draft.polling_allowed
                    ? "yes"
                    : "no"
              }
              onChange={(e) =>
                setDraft({
                  ...draft,
                  polling_allowed:
                    e.target.value === "default" ? null : e.target.value === "yes",
                })
              }
            >
              <option value="default">{t("admin.platformTiers.pollingDefault")}</option>
              <option value="yes">{t("admin.platformTiers.pollingYes")}</option>
              <option value="no">{t("admin.platformTiers.pollingNo")}</option>
            </select>
          </label>
        </div>
        <div className="modal-foot">
          <Button onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" onClick={() => void save()} disabled={busy || !draft.name.trim()}>
            {busy ? t("common.saving") : t("admin.platformTiers.save")}
          </Button>
        </div>
      </div>
    </div>,
    document.body,
  );
}

