// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { Pencil, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import {
  api,
  type EffectiveLimits,
  type PlatformTier,
  type TenantEntitlement,
} from "../api";
import { explainApiError } from "../lib/explainApiError";
import { formatDate } from "../lib/datetime";
import { Button } from "./Button";
import { ErrorNotice } from "./ErrorNotice";

// PlanLimitsSection shows an org's effective plan + limits and lets a
// platform admin assign a tier, grant/force a plan (trial, comp, force
// free/pro), and override individual limits on top. Self-fetching so the
// org detail page just drops it in.
export function PlanLimitsSection({ tenant }: { tenant: string }) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [eff, setEff] = useState<EffectiveLimits | null>(null);
  const [ent, setEnt] = useState<TenantEntitlement | null>(null);
  const [tiers, setTiers] = useState<PlatformTier[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);

  const refresh = useCallback(async () => {
    if (!token) return;
    try {
      const r = await api.platformGetEntitlement(token, tenant);
      setEnt(r.entitlement);
      setEff(r.effective);
      setTiers(r.tiers ?? []);
      setError(null);
    } catch (e) {
      setError(explainApiError(e, t));
    }
  }, [token, tenant, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const fmt = (v: number) => (v > 0 ? String(v) : t("admin.platformPlan.unlimited"));
  const disk =
    eff && eff.disk_quota_bytes > 0
      ? `${Math.round(eff.disk_quota_bytes / (1024 * 1024))} MB`
      : t("admin.platformPlan.unlimited");

  return (
    <div style={{ marginTop: "var(--space-4)" }}>
      <div className="pa-section-head">
        <h2 className="admin-section-head" style={{ margin: 0 }}>
          {t("admin.platformPlan.head")}
        </h2>
        <Button onClick={() => setEditing(true)} disabled={!ent}>
          <Pencil size={13} style={{ marginRight: 4 }} />
          {t("admin.platformPlan.edit")}
        </Button>
      </div>
      {error && <ErrorNotice>{error}</ErrorNotice>}
      {eff && (
        <div className="card">
          <dl className="kv-grid">
            <dt>{t("admin.platformPlan.plan")}</dt>
            <dd>
              <span className={"count-pill" + (eff.plan === "pro" ? " active" : "")}>{eff.plan}</span>
              {eff.comped && <span className="pa-subtext"> · {t("admin.platformPlan.comped")}</span>}
              {eff.trial_ends_at && (
                <span className="pa-subtext"> · {t("admin.platformPlan.trialUntil", { date: formatDate(eff.trial_ends_at) })}</span>
              )}
            </dd>
            <dt>{t("admin.platformPlan.tier")}</dt>
            <dd>{eff.tier_id || "—"}</dd>
            <dt>{t("admin.platformTiers.runs")}</dt>
            <dd>{fmt(eff.runs_per_month)}</dd>
            <dt>{t("admin.platformTiers.members")}</dt>
            <dd>{fmt(eff.max_members)}</dd>
            <dt>{t("admin.platformTiers.concurrency")}</dt>
            <dd>{fmt(eff.max_concurrency)}</dd>
            <dt>{t("admin.platformTiers.retention")}</dt>
            <dd>{eff.retention_days > 0 ? `${eff.retention_days}d` : t("admin.platformPlan.unlimited")}</dd>
            <dt>{t("admin.platformTiers.nodes")}</dt>
            <dd>{fmt(eff.max_graph_nodes)}</dd>
            <dt>{t("admin.platformTiers.flows")}</dt>
            <dd>{fmt(eff.max_flows)}</dd>
            <dt>{t("admin.platformTiers.timeout")}</dt>
            <dd>{eff.max_timeout_seconds > 0 ? `${eff.max_timeout_seconds}s` : t("admin.platformPlan.unlimited")}</dd>
            <dt>{t("admin.platformTiers.disk")}</dt>
            <dd>{disk}</dd>
            <dt>{t("admin.platformTiers.polling")}</dt>
            <dd>{eff.polling_allowed ? t("common.yes") : t("common.no")}</dd>
          </dl>
        </div>
      )}
      {editing && ent && (
        <EntitlementEditor
          tenant={tenant}
          ent={ent}
          tiers={tiers}
          onClose={() => setEditing(false)}
          onSaved={() => {
            setEditing(false);
            void refresh();
          }}
          onError={setError}
        />
      )}
    </div>
  );
}

// numOrNull maps the override inputs: "" → null (inherit), else a number.
function numOrNull(s: string): number | null {
  const trimmed = s.trim();
  if (trimmed === "") return null;
  const n = Number(trimmed);
  return Number.isFinite(n) ? n : null;
}

function EntitlementEditor({
  tenant,
  ent,
  tiers,
  onClose,
  onSaved,
  onError,
}: {
  tenant: string;
  ent: TenantEntitlement;
  tiers: PlatformTier[];
  onClose: () => void;
  onSaved: () => void;
  onError: (msg: string) => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [busy, setBusy] = useState(false);
  const [tierID, setTierID] = useState(ent.tier_id || "free");
  // plan grant radio: "" inherit, "free", "pro"
  const [planOverride, setPlanOverride] = useState(ent.plan_override ?? "");
  const [comped, setComped] = useState(!!ent.comped);
  const [trial, setTrial] = useState(ent.trial_ends_at ? ent.trial_ends_at.slice(0, 10) : "");
  const str = (v: number | null | undefined) => (v === null || v === undefined ? "" : String(v));
  const [runs, setRuns] = useState(str(ent.runs_per_month));
  const [members, setMembers] = useState(str(ent.max_members));
  const [concurrency, setConcurrency] = useState(str(ent.max_concurrency));
  const [retention, setRetention] = useState(str(ent.retention_days));
  const [nodes, setNodes] = useState(str(ent.max_graph_nodes));
  const [flows, setFlows] = useState(str(ent.max_flows));
  const [timeout, setTimeoutS] = useState(str(ent.max_timeout_seconds));
  const [diskMB, setDiskMB] = useState(
    ent.disk_quota_bytes ? String(Math.round(ent.disk_quota_bytes / (1024 * 1024))) : "",
  );
  // polling override tri-state: "inherit" | "on" | "off"
  const [polling, setPolling] = useState(
    ent.polling_allowed === null || ent.polling_allowed === undefined
      ? "inherit"
      : ent.polling_allowed
        ? "on"
        : "off",
  );
  const [notes, setNotes] = useState(ent.notes ?? "");

  const save = async () => {
    if (!token) return;
    setBusy(true);
    const diskBytes = numOrNull(diskMB);
    const body: TenantEntitlement = {
      tenant,
      tier_id: tierID,
      plan_override: planOverride,
      comped,
      trial_ends_at: trial ? new Date(trial + "T00:00:00Z").toISOString() : null,
      runs_per_month: numOrNull(runs),
      max_members: numOrNull(members),
      max_concurrency: numOrNull(concurrency),
      retention_days: numOrNull(retention),
      max_graph_nodes: numOrNull(nodes),
      max_flows: numOrNull(flows),
      max_timeout_seconds: numOrNull(timeout),
      disk_quota_bytes: diskBytes === null ? null : diskBytes * 1024 * 1024,
      polling_allowed: polling === "inherit" ? null : polling === "on",
      notes,
    };
    try {
      await api.platformPutEntitlement(token, tenant, body);
      onSaved();
    } catch (e) {
      onError(explainApiError(e, t));
      setBusy(false);
    }
  };

  const overrideInput = (label: string, value: string, set: (s: string) => void) => (
    <label>
      {label} <span className="pa-subtext">({t("admin.platformPlan.blankInherit")})</span>
      <input type="number" min={0} value={value} onChange={(e) => set(e.target.value)} />
    </label>
  );

  return createPortal(
    <div className="settings-backdrop" onClick={() => !busy && onClose()}>
      <div className="settings-dialog" onClick={(e) => e.stopPropagation()} role="dialog" aria-modal="true">
        <div className="settings-head">
          <h2>{t("admin.platformPlan.editTitle")}</h2>
          <Button variant="ghost" size="icon" onClick={onClose} disabled={busy}>
            <X size={18} />
          </Button>
        </div>
        <div className="settings-body pa-tier-form">
          <label>
            {t("admin.platformPlan.tier")}
            <select value={tierID} onChange={(e) => setTierID(e.target.value)}>
              {tiers.map((tr) => (
                <option key={tr.id} value={tr.id}>
                  {tr.name} ({tr.plan})
                </option>
              ))}
            </select>
          </label>
          <label>
            {t("admin.platformPlan.planGrant")}
            <select value={planOverride} onChange={(e) => setPlanOverride(e.target.value)}>
              <option value="">{t("admin.platformPlan.planInherit")}</option>
              <option value="free">{t("admin.platformPlan.forceFree")}</option>
              <option value="pro">{t("admin.platformPlan.forcePro")}</option>
            </select>
          </label>
          <label className="pa-tier-check">
            <input type="checkbox" checked={comped} onChange={(e) => setComped(e.target.checked)} />
            {t("admin.platformPlan.compedLabel")}
          </label>
          <label>
            {t("admin.platformPlan.trialEnds")} <span className="pa-subtext">({t("admin.platformPlan.blankNone")})</span>
            <input type="date" value={trial} onChange={(e) => setTrial(e.target.value)} />
          </label>
          {overrideInput(t("admin.platformTiers.runs"), runs, setRuns)}
          {overrideInput(t("admin.platformTiers.members"), members, setMembers)}
          {overrideInput(t("admin.platformTiers.concurrency"), concurrency, setConcurrency)}
          {overrideInput(t("admin.platformTiers.retention"), retention, setRetention)}
          {overrideInput(t("admin.platformTiers.nodes"), nodes, setNodes)}
          {overrideInput(t("admin.platformTiers.flows"), flows, setFlows)}
          {overrideInput(t("admin.platformTiers.timeout"), timeout, setTimeoutS)}
          {overrideInput(t("admin.platformTiers.diskMB"), diskMB, setDiskMB)}
          <label>
            {t("admin.platformTiers.polling")}
            <select value={polling} onChange={(e) => setPolling(e.target.value)}>
              <option value="inherit">{t("admin.platformPlan.planInherit")}</option>
              <option value="on">{t("common.yes")}</option>
              <option value="off">{t("common.no")}</option>
            </select>
          </label>
          <label>
            {t("admin.platformPlan.notes")}
            <input type="text" value={notes} onChange={(e) => setNotes(e.target.value)} />
          </label>
        </div>
        <div className="settings-foot">
          <Button onClick={onClose} disabled={busy}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" onClick={() => void save()} disabled={busy}>
            {busy ? t("admin.platformTiers.saving") : t("admin.platformTiers.save")}
          </Button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
