import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Plus, Workflow, Lock, Globe } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import { iconFor, isBrandedIcon } from "../icons";
import { shouldShowTenantID } from "../lib/visibleTenant";
import type { FlowSummary } from "../types";

export function FlowList() {
  const { t } = useTranslation();
  const { token, me, tenants, activeTenant, activeWorkspace } = useAuth();
  const [flows, setFlows] = useState<FlowSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const navigate = useNavigate();

  useEffect(() => {
    if (!token || !me || !activeWorkspace) return;
    let cancelled = false;
    setLoading(true);
    api
      .listGraphs(token, activeTenant, activeWorkspace)
      .then((r) => {
        if (!cancelled) setFlows(r.graphs ?? []);
      })
      .catch((e) => {
        if (!cancelled) setError((e as Error).message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [token, me, activeWorkspace]);

  // createNew is called by the modal with a human name (+ optional
  // description). The machine ID is derived from the name so the user
  // never types a slug — a short random suffix keeps it unique.
  const createNew = async (name: string, description: string) => {
    if (!token || !me || !activeWorkspace) return;
    const id = `${slugify(name)}-${Math.random().toString(36).slice(2, 8)}`;
    await api.saveGraph(token, {
      id,
      tenant: activeTenant,
      workspace: activeWorkspace,
      nodes: [],
      edges: [],
      name: name.trim(),
      description: description.trim() || undefined,
    });
    navigate(`/flows/${encodeURIComponent(id)}`);
  };

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("flowList.title")}</h1>
          <div className="sub">
            {shouldShowTenantID(me, tenants.length)
              ? `${activeTenant || me?.tenant}/${activeWorkspace}`
              : activeWorkspace}
          </div>
        </div>
        <div style={{ display: "flex", gap: 8 }}>
          <Link
            to="/templates"
            style={{ textDecoration: "none" }}
            className="secondary-link"
          >
            <button type="button" className="secondary">
              {t("flowList.fromTemplate")}
            </button>
          </Link>
          <button className="primary" onClick={() => setCreating(true)}>
            <Plus size={16} style={{ marginRight: 6, verticalAlign: -3 }} />
            {t("flowList.newFlow")}
          </button>
        </div>
      </div>
      {loading && <div className="card">{t("common.loading")}</div>}
      {error && <div className="card" style={{ color: "var(--danger)" }}>{error}</div>}
      {!loading && !error && flows.length === 0 && (
        <div className="card flow-empty">
          <Workflow size={28} className="flow-empty-icon" />
          <h2>{t("flowList.emptyTitle")}</h2>
          <p>{t("flowList.emptyBody")}</p>
          <div className="flow-empty-actions">
            <button
              type="button"
              className="primary"
              onClick={() => navigate("/templates")}
            >
              {t("flowList.emptyTemplateCta")}
            </button>
            <button type="button" onClick={() => setCreating(true)}>
              {t("flowList.emptyBlankCta")}
            </button>
          </div>
        </div>
      )}
      {creating && (
        <NewFlowModal
          onCancel={() => setCreating(false)}
          onCreate={async (name, description) => {
            await createNew(name, description);
          }}
        />
      )}
      <div className="graph-list">
        {flows.map((f) => {
          const isPrivate = f.visibility === "private";
          const ownedByMe = !!me && f.owner === me.subject;
          const Icon = f.icon ? iconFor(f.icon) : Workflow;
          const displayName = f.name || f.id;
          return (
            <Link
              key={f.id}
              to={`/flows/${encodeURIComponent(f.id)}`}
              style={{ textDecoration: "none", color: "inherit" }}
            >
              <div className="graph-card">
                <div className="name">
                  <Icon
                    size={isBrandedIcon(f.icon) ? 20 : 16}
                    color={isBrandedIcon(f.icon) ? undefined : "currentColor"}
                  />
                  <span style={{ flex: 1, minWidth: 0 }}>
                    <span style={{ display: "block" }}>{displayName}</span>
                    {f.name && (
                      <span
                        style={{
                          fontFamily: "var(--font-mono)",
                          fontSize: 11,
                          color: "var(--faint)",
                        }}
                      >
                        {f.id}
                      </span>
                    )}
                  </span>
                  {isPrivate ? (
                    <span
                      className="vis-badge private"
                      title={
                        ownedByMe
                          ? t("flowList.privateOwnedByYou")
                          : t("flowList.privateOwnedBy", {
                              owner: f.owner ?? t("common.unknownParen"),
                            })
                      }
                    >
                      <Lock size={11} />
                      {t("common.private")}
                    </span>
                  ) : (
                    <span
                      className="vis-badge org"
                      title={t("flowList.orgTooltip")}
                    >
                      <Globe size={11} />
                      {t("common.org")}
                    </span>
                  )}
                </div>
                {f.description && (
                  <div
                    className="meta"
                    style={{ color: "var(--muted)", lineHeight: 1.4 }}
                  >
                    {f.description}
                  </div>
                )}
                <div className="meta">
                  {f.owner && (
                    <>
                      {t("flowList.ownerLabel")} <code>{f.owner}</code>
                    </>
                  )}
                </div>
              </div>
            </Link>
          );
        })}
      </div>
    </div>
  );
}

// slugify turns a human flow name into the [A-Za-z0-9_.-] machine ID the
// daemon expects. Lowercases, swaps runs of anything else for a single
// hyphen, trims edge hyphens, and falls back to "flow" when a name is
// all punctuation/empty.
function slugify(name: string): string {
  const s = name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return s || "flow";
}

// NewFlowModal collects a friendly name (+ optional description) for a
// blank flow. No ID field — the parent derives the machine ID from the
// name. Reuses the shared settings-dialog modal chrome.
function NewFlowModal({
  onCancel,
  onCreate,
}: {
  onCancel: () => void;
  onCreate: (name: string, description: string) => Promise<void>;
}) {
  const { t } = useTranslation();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim() || busy) return;
    setBusy(true);
    setErr(null);
    try {
      await onCreate(name, description);
    } catch (e) {
      setErr((e as Error).message);
      setBusy(false);
    }
  };

  return (
    <div className="settings-backdrop" onClick={onCancel}>
      <form
        className="settings-dialog"
        style={{ maxWidth: 460 }}
        onClick={(e) => e.stopPropagation()}
        onSubmit={submit}
      >
        <div className="settings-head">
          <h2>{t("flowList.newModalTitle")}</h2>
        </div>
        <div className="settings-body">
          <div className="sf-field">
            <div className="label-row">
              <label htmlFor="new-flow-name">{t("flowList.nameLabel")}</label>
            </div>
            <input
              id="new-flow-name"
              autoFocus
              value={name}
              placeholder={t("flowList.namePlaceholder")}
              onChange={(e) => setName(e.target.value)}
            />
          </div>
          <div className="sf-field">
            <div className="label-row">
              <label htmlFor="new-flow-desc">{t("flowList.descLabel")}</label>
            </div>
            <input
              id="new-flow-desc"
              value={description}
              placeholder={t("flowList.descPlaceholder")}
              onChange={(e) => setDescription(e.target.value)}
            />
          </div>
          {err && (
            <div style={{ color: "var(--danger)", fontSize: 12 }}>{err}</div>
          )}
        </div>
        <div className="settings-foot">
          <button type="button" onClick={onCancel}>
            {t("common.cancel")}
          </button>
          <button type="submit" className="primary" disabled={busy || !name.trim()}>
            {busy ? t("flowList.creating") : t("flowList.createCta")}
          </button>
        </div>
      </form>
    </div>
  );
}
