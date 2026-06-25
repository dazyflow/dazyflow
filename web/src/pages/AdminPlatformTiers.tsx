import { useCallback, useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { AlertCircle, Layers, Pencil, Plus, Trash2, X } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, type PlatformTier } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { Button } from "../components/Button";
import { ConfirmModal } from "../components/ConfirmModal";

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
      <div className="card" style={{ color: "var(--danger)" }}>
        <Trans i18nKey="admin.platform.needPlatformAdmin" components={[<code />]} />
      </div>
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
    polling_allowed: true,
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
          <Layers size={20} />
          <div>
            <h1>{t("admin.platformTiers.title")}</h1>
            <div className="sub">{t("admin.platformTiers.subtitle")}</div>
          </div>
        </div>
        <Button variant="primary" onClick={() => setEditing(blankTier())}>
          <Plus size={14} style={{ marginRight: 6 }} />
          {t("admin.platformTiers.new")}
        </Button>
      </div>

      {error && (
        <div className="card error" style={{ marginBottom: "var(--space-3)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}

      {loading ? (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("common.loading")}
        </div>
      ) : (
        <div className="user-list">
          {tiers.map((tier) => (
            <div className="user-card" key={tier.id}>
              <div style={{ minWidth: 0 }}>
                <div className="subject">
                  {tier.name}
                  <span
                    className={"count-pill" + (tier.plan === "pro" ? " active" : "")}
                    style={{ marginLeft: 8 }}
                  >
                    {tier.plan}
                  </span>
                  {tier.built_in && (
                    <span className="count-pill" style={{ marginLeft: 8 }}>
                      {t("admin.platformTiers.builtIn")}
                    </span>
                  )}
                </div>
                <div className="meta">{limitSummary(tier, (k) => t(k))}</div>
              </div>
              <div className="user-card-actions" style={{ flexDirection: "row" }}>
                <Button onClick={() => setEditing(tier)} title={t("common.edit", "Edit")}>
                  <Pencil size={13} />
                </Button>
                {!tier.built_in && (
                  <Button variant="danger" onClick={() => setConfirmDelete(tier)}>
                    <Trash2 size={13} />
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
          confirmLabel={t("admin.platformTiers.delete")}
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
    `${t("admin.platformTiers.nodes")}: ${n(tier.max_graph_nodes)}`,
    `${t("admin.platformTiers.flows")}: ${n(tier.max_flows)}`,
    `${t("admin.platformTiers.disk")}: ${mb}`,
    `${t("admin.platformTiers.polling")}: ${tier.polling_allowed ? t("common.yes") : t("common.no")}`,
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

  return createPortal(
    <div className="settings-backdrop" onClick={() => !busy && onClose()}>
      <div className="settings-dialog" onClick={(e) => e.stopPropagation()} role="dialog" aria-modal="true">
        <div className="settings-head">
          <h2>{isNew ? t("admin.platformTiers.new") : t("admin.platformTiers.edit", { name: tier.name })}</h2>
          <Button variant="ghost" size="icon" onClick={onClose} disabled={busy}>
            <X size={18} />
          </Button>
        </div>
        <div className="settings-body pa-tier-form">
          <label>
            {t("admin.platformTiers.name")}
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
              <option value="free">free</option>
              <option value="pro">pro</option>
            </select>
          </label>
          <label>
            {t("admin.platformTiers.runs")} <span className="pa-subtext">({t("admin.platformTiers.zeroInherit")})</span>
            {num("runs_per_month")}
          </label>
          <label>
            {t("admin.platformTiers.nodes")} <span className="pa-subtext">({t("admin.platformTiers.zeroInherit")})</span>
            {num("max_graph_nodes")}
          </label>
          <label>
            {t("admin.platformTiers.flows")} <span className="pa-subtext">({t("admin.platformTiers.zeroInherit")})</span>
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
          <label className="pa-tier-check">
            <input
              type="checkbox"
              checked={draft.polling_allowed}
              onChange={(e) => setDraft({ ...draft, polling_allowed: e.target.checked })}
            />
            {t("admin.platformTiers.pollingAllowed")}
          </label>
        </div>
        <div className="settings-foot">
          <Button onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" onClick={() => void save()} disabled={busy || !draft.name.trim()}>
            {busy ? t("admin.platformTiers.saving") : t("admin.platformTiers.save")}
          </Button>
        </div>
      </div>
    </div>,
    document.body,
  );
}

function slugify(name: string): string {
  return name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}
