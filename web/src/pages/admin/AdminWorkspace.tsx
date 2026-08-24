// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import { AlertCircle, Building2, Check, Globe, Settings2 } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../../auth";
import { api, APIError } from "../../api";
import { IconUpload } from "../../components/fields/IconUpload";
import { Button } from "../../components/ui/Button";
import type { WorkspaceLimits } from "../../types";
import { explainApiError } from "../../lib/explainApiError";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { ICON } from "../../icons";
import { formatBytes } from "../../lib/format";
import { Loading } from "../../components/ui/Loading";

// AdminWorkspace is a read-only view of the effective limits for the
// caller's tenant: the per-tenant disk quota plus the daemon-wide graph
// caps. There's no write side — these are operator-configured (flags), so
// the page surfaces them rather than implying they're editable here.
export function AdminWorkspace() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const [limits, setLimits] = useState<WorkspaceLimits | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      setLimits(await api.getWorkspaceLimits(token));
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

  if (!hasPerm("organization:admin")) {
    return (
      <ErrorNotice>
        <Trans i18nKey="admin.workspace.needAdmin" components={[<code />]} />
      </ErrorNotice>
    );
  }

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <Settings2 size={ICON.xl} />
            {t("admin.workspace.title")}
          </h1>
          <div className="sub">{t("admin.workspace.subtitle")}</div>
        </div>
      </div>

      {error && (
        <ErrorNotice style={{ marginBottom: "var(--space-4)" }}>
{error}
        </ErrorNotice>
      )}
      {loading && !limits && (
        <Loading />
      )}

      <OrgProfileEditor />

      {limits && (
        <>
          <div className="card" style={{ marginBottom: "var(--space-4)" }}>
            <h3 style={{ marginTop: 0 }}>{t("admin.workspace.diskQuota")}</h3>
            {limits.quota ? (
              <Row
                label={t("admin.workspace.usage")}
                value={
                  formatBytes(limits.quota.used_bytes ?? 0) +
                  " / " +
                  (limits.quota.limit_bytes > 0
                    ? formatBytes(limits.quota.limit_bytes)
                    : t("admin.workspace.unlimited"))
                }
              />
            ) : (
              <div className="muted">{t("admin.workspace.noQuota")}</div>
            )}
          </div>

          <div className="card">
            <h3 style={{ marginTop: 0 }}>{t("admin.workspace.graphLimits")}</h3>
            <div className="muted" style={{ fontSize: "var(--text-sm)", marginBottom: "var(--space-2)" }}>
              {t("admin.workspace.daemonWideNote")}
            </div>
            <Row
              label={t("admin.workspace.maxGraphNodes")}
              value={limits.max_graph_nodes > 0 ? String(limits.max_graph_nodes) : t("admin.workspace.unlimited")}
            />
            <Row
              label={t("admin.workspace.maxTimeout")}
              value={fmtSeconds(t, limits.max_graph_timeout_seconds)}
            />
          </div>
        </>
      )}
    </div>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div style={{ display: "flex", justifyContent: "space-between", padding: "var(--space-1h) 0", borderTop: "1px solid var(--border)" }}>
      <span className="muted">{label}</span>
      <span style={{ fontFamily: "var(--font-mono)" }}>{value}</span>
    </div>
  );
}

function fmtSeconds(t: (k: string) => string, s: number): string {
  return s > 0 ? `${s}s` : t("admin.workspace.none");
}

// OrgProfileEditor lets the owner rename their organization. The
// underlying tenant ID is shown in fine print because it still appears
// in webhook URLs and audit entries — the rename only changes the
// display label. Defaulted from the email domain on signup so a fresh
// account doesn't surface "usr_de3d2365" anywhere.
function OrgProfileEditor() {
  const { t } = useTranslation();
  const { token, me, refreshMe } = useAuth();
  const [displayName, setDisplayName] = useState("");
  const [icon, setIcon] = useState<string | undefined>(undefined);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [savedAt, setSavedAt] = useState<Date | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Subdomain (per-org wildcard host) state. wildcardDomain is empty unless the
  // deploy enabled the feature, in which case the section renders. savedSub is
  // the persisted label; sub is the current input. avail tracks the live
  // availability probe so the user learns "taken"/"invalid" before saving.
  const [wildcardDomain, setWildcardDomain] = useState("");
  const [sub, setSub] = useState("");
  const [savedSub, setSavedSub] = useState("");
  const [savingSub, setSavingSub] = useState(false);
  const [subSavedAt, setSubSavedAt] = useState<Date | null>(null);
  const [subError, setSubError] = useState<string | null>(null);
  const [avail, setAvail] = useState<
    "idle" | "checking" | "ok" | "taken" | "invalid" | "current"
  >("idle");
  // loadedRef gates autosave until the initial GET completes; savedRef
  // holds the last-persisted values so the autosave effect only fires on
  // a genuine user edit (not the load itself).
  const loadedRef = useRef(false);
  const savedRef = useRef<{ name: string; icon?: string }>({ name: "", icon: undefined });

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const p = await api.getOrgProfile(token);
      setDisplayName(p.display_name ?? "");
      setIcon(p.icon || undefined);
      setWildcardDomain(p.wildcard_domain ?? "");
      setSub(p.subdomain ?? "");
      setSavedSub(p.subdomain ?? "");
      savedRef.current = { name: (p.display_name ?? "").trim(), icon: p.icon || undefined };
      loadedRef.current = true;
      setError(null);
    } catch (e) {
      if (e instanceof APIError && e.status === 501) {
        setError(t("admin.workspace.profileNotConfigured"));
      } else {
        setError(explainApiError(e, t));
      }
    } finally {
      setLoading(false);
    }
  }, [token, t]);
  useEffect(() => {
    void refresh();
  }, [refresh]);

  const save = useCallback(async () => {
    if (!token) return;
    setSaving(true);
    setError(null);
    try {
      await api.putOrgProfile(token, displayName.trim(), icon);
      savedRef.current = { name: displayName.trim(), icon };
      setSavedAt(new Date());
      // Refresh identity so the switcher + top bar pick up the new
      // name/icon immediately (updates the context's `me`, not just a
      // throwaway fetch). The session itself doesn't change, just labels.
      await refreshMe();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setSaving(false);
    }
  }, [token, displayName, icon, refreshMe, t]);

  // Live subdomain availability: debounce a probe as the user types so they
  // learn "taken"/"invalid" before saving. An unchanged value reads as the
  // current claim (no probe needed); an empty value is the "clear it" case.
  useEffect(() => {
    if (!token || !wildcardDomain) return;
    const label = sub.trim().toLowerCase();
    if (label === savedSub) {
      setAvail("current");
      return;
    }
    if (label === "") {
      setAvail("idle");
      return;
    }
    setAvail("checking");
    let cancelled = false;
    const h = window.setTimeout(() => {
      api
        .checkSubdomainAvailable(token, label)
        .then((r) => {
          if (cancelled) return;
          if (r.available) setAvail("ok");
          else setAvail(r.reason === "invalid" ? "invalid" : "taken");
        })
        .catch(() => {
          if (!cancelled) setAvail("idle");
        });
    }, 400);
    return () => {
      cancelled = true;
      window.clearTimeout(h);
    };
  }, [sub, savedSub, token, wildcardDomain]);

  const saveSubdomain = useCallback(async () => {
    if (!token) return;
    const label = sub.trim().toLowerCase();
    setSavingSub(true);
    setSubError(null);
    try {
      const r = await api.putOrgSubdomain(token, label);
      setSavedSub(r.subdomain);
      setSub(r.subdomain);
      setSubSavedAt(new Date());
      setAvail("current");
    } catch (e) {
      // 409 taken / 400 invalid carry friendly server messages; the generic
      // mapper handles everything else.
      setSubError(explainApiError(e, t));
    } finally {
      setSavingSub(false);
    }
  }, [token, sub, t]);

  // Autosave: debounce-persist a genuine change (skipped until the
  // initial load, and when the values already match what's stored).
  useEffect(() => {
    if (!loadedRef.current) return;
    if (
      displayName.trim() === savedRef.current.name &&
      (icon ?? "") === (savedRef.current.icon ?? "")
    ) {
      return;
    }
    const h = window.setTimeout(() => void save(), 600);
    return () => window.clearTimeout(h);
  }, [displayName, icon, save]);

  if (loading) return null;
  // Load failure (e.g. 501 not-configured) replaces the editor; a
  // transient save error renders inline below instead.
  if (error && !loadedRef.current) {
    return (
      <div className="card" style={{ marginBottom: "var(--space-4)" }}>
        <h3 style={{ marginTop: 0 }}>{t("admin.workspace.orgNameHead")}</h3>
        <div className="desc muted">{error}</div>
      </div>
    );
  }

  return (
    <div className="card" style={{ marginBottom: "var(--space-4)" }}>
      <h3 style={{ marginTop: 0 }}>{t("admin.workspace.orgNameHead")}</h3>
      <p className="desc">{t("admin.workspace.orgNameDesc")}</p>
      <div className="sf-field">
        <label>{t("admin.workspace.orgIconLabel")}</label>
        <IconUpload
          value={icon}
          onChange={setIcon}
          fallback={<Building2 size={22} />}
        />
      </div>
      <div className="sf-field">
        <label>{t("admin.workspace.orgNameLabel")}</label>
        <input
          type="text"
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
          placeholder={t("admin.workspace.orgNamePlaceholder")}
          maxLength={80}
        />
        <div className="desc">
          <Trans
            i18nKey="admin.workspace.orgIdHint"
            values={{ tenant: me?.tenant ?? "" }}
            components={[<code />]}
          />
        </div>
      </div>

      {/* Subdomain — only when the deployment enabled per-org wildcard hosts. */}
      {wildcardDomain && (
        <div className="sf-field" style={{ marginTop: "var(--space-4)" }}>
          <label>
            <Globe className="icon-inline" size={ICON.sm} />{" "}
            {t("admin.workspace.subdomainLabel")}
          </label>
          <p className="desc">{t("admin.workspace.subdomainDesc")}</p>
          <div className="subdomain-row">
            <input
              type="text"
              className="subdomain-input"
              value={sub}
              onChange={(e) =>
                setSub(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ""))
              }
              placeholder={t("admin.workspace.subdomainPlaceholder")}
              maxLength={63}
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
            />
            <span className="subdomain-suffix">.{wildcardDomain}</span>
          </div>
          {/* Live availability + the resulting URL. */}
          <div className="subdomain-status">
            {avail === "checking" && (
              <span className="desc">{t("admin.workspace.subdomainChecking")}</span>
            )}
            {avail === "ok" && (
              <span className="desc" style={{ color: "var(--success)" }}>
                <Check className="icon-inline" size={ICON.xs} />{" "}
                {t("admin.workspace.subdomainAvailable")}
              </span>
            )}
            {avail === "taken" && (
              <span className="desc" style={{ color: "var(--danger)" }}>
                {t("admin.workspace.subdomainTaken")}
              </span>
            )}
            {avail === "invalid" && (
              <span className="desc" style={{ color: "var(--danger)" }}>
                {t("admin.workspace.subdomainInvalid")}
              </span>
            )}
            {avail === "current" && savedSub && (
              <span className="desc">
                <Trans
                  i18nKey="admin.workspace.subdomainCurrent"
                  values={{ url: `${savedSub}.${wildcardDomain}` }}
                  components={[<code />]}
                />
              </span>
            )}
          </div>
          <div style={{ marginTop: "var(--space-2)", display: "flex", gap: "var(--space-2)", alignItems: "center" }}>
            <Button
              variant="primary"
              onClick={() => void saveSubdomain()}
              disabled={
                savingSub ||
                sub.trim().toLowerCase() === savedSub ||
                avail === "checking" ||
                avail === "taken" ||
                avail === "invalid"
              }
            >
              {savingSub
                ? t("common.saving")
                : sub.trim() === "" && savedSub
                ? t("admin.workspace.subdomainRelease")
                : t("admin.workspace.subdomainSave")}
            </Button>
            {subSavedAt && !subError && (
              <span className="desc" style={{ color: "var(--success)" }}>
                <Check className="icon-inline" size={ICON.xs} /> {t("common.saved")}
              </span>
            )}
          </div>
          {subError && (
            <div className="error" style={{ marginTop: "var(--space-2)" }}>
              <AlertCircle className="icon-inline" size={ICON.sm} /> {subError}
            </div>
          )}
        </div>
      )}

      <div className="modal-foot" style={{ borderTop: "none", padding: 0, minHeight: 18 }}>
        {saving ? (
          <span className="desc">{t("common.saving")}</span>
        ) : savedAt ? (
          <span className="desc" style={{ color: "var(--success)" }}>
            <Check className="icon-inline" size={ICON.xs} /> {t("common.saved")}
          </span>
        ) : null}
      </div>
      {error && (
        <div className="error" style={{ marginTop: "var(--space-3)" }}>
          <AlertCircle className="icon-inline" size={ICON.sm} /> {error}
        </div>
      )}
    </div>
  );
}

