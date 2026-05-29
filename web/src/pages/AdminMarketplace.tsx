import { useCallback, useEffect, useState, type ReactNode } from "react";
import {
  Store,
  GitBranch,
  Download,
  AlertCircle,
  RefreshCw,
  Trash2,
  ShieldAlert,
  Eye,
} from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import type {
  InstalledIntegration,
  InstalledDrop,
  IntegrationPreview,
  DropCapabilitySummary,
  TrustTier,
} from "../types";

// AdminMarketplace is the platform-admin surface for installing integrations
// and drops from a git repo@tag. Installing an integration ("google")
// registers its provider so org admins can connect; drops are gated on their
// required integrations being installed first. The trust tier shown on each
// item is signature-derived (see the daemon's Keyring), not self-declared.
export function AdminMarketplace() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const [integrations, setIntegrations] = useState<InstalledIntegration[]>([]);
  const [drops, setDrops] = useState<InstalledDrop[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const [i, d] = await Promise.all([
        api.listMarketplaceIntegrations(token),
        api.listMarketplaceDrops(token),
      ]);
      setIntegrations(i.integrations ?? []);
      setDrops(d.drops ?? []);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const removeIntegration = async (id: string) => {
    if (!token) return;
    try {
      await api.uninstallIntegration(token, id);
      setError(null);
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    }
  };
  const removeDrop = async (id: string) => {
    if (!token) return;
    try {
      await api.uninstallDrop(token, id);
      setError(null);
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    }
  };

  if (!hasPerm("platform:admin")) {
    return (
      <div className="card" style={{ color: "var(--danger)" }}>
        <Trans i18nKey="admin.marketplace.needAdmin" components={[<code />]} />
      </div>
    );
  }

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>{t("admin.marketplace.title")}</h1>
          <div className="sub">{t("admin.marketplace.subtitle")}</div>
        </div>
        <button className="ghost" onClick={() => void refresh()} disabled={loading}>
          <RefreshCw size={14} /> {t("admin.marketplace.refresh")}
        </button>
      </div>

      {error && (
        <div className="card error" style={{ marginBottom: 12, color: "var(--danger)" }}>
          <AlertCircle size={14} /> {error}
        </div>
      )}

      <InstallIntegration token={token} onInstalled={refresh} />

      <Section icon={<Store size={16} />} title={t("admin.marketplace.installedIntegrations")}>
        {integrations.length === 0 ? (
          <div className="sub">{t("admin.marketplace.noIntegrations")}</div>
        ) : (
          <ul className="mk-list">
            {integrations.map((it) => (
              <li key={it.id} className="mk-row">
                <span className="mk-id">{it.id}</span>
                <span className="sub">{it.version}</span>
                <TierBadge tier={it.tier} />
                <button className="ghost" onClick={() => void removeIntegration(it.id)}>
                  <Trash2 size={14} /> {t("admin.marketplace.uninstall")}
                </button>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <InstallDrop token={token} onInstalled={refresh} />

      <Section icon={<Download size={16} />} title={t("admin.marketplace.installedDrops")}>
        {drops.length === 0 ? (
          <div className="sub">{t("admin.marketplace.noDrops")}</div>
        ) : (
          <ul className="mk-list">
            {drops.map((d) => (
              <li key={d.id} className="mk-row">
                <span className="mk-id">{d.manifest?.label || d.id}</span>
                <span className="sub">{d.manifest?.integration || d.id}</span>
                <TierBadge tier={d.tier} />
                <button className="ghost" onClick={() => void removeDrop(d.id)}>
                  <Trash2 size={14} /> {t("admin.marketplace.uninstall")}
                </button>
              </li>
            ))}
          </ul>
        )}
      </Section>
    </div>
  );
}

// InstallIntegration: paste repo+ref → Preview (fetch the setup form + tier +
// commit) → fill credentials → Install.
function InstallIntegration({
  token,
  onInstalled,
}: {
  token: string | null;
  onInstalled: () => void;
}) {
  const { t } = useTranslation();
  const [repo, setRepo] = useState("");
  const [ref, setRef] = useState("");
  const [preview, setPreview] = useState<IntegrationPreview | null>(null);
  const [creds, setCreds] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const doPreview = async () => {
    if (!token || !repo.trim() || !ref.trim()) return;
    setBusy(true);
    setErr(null);
    try {
      const p = await api.previewIntegrationFromGit(token, repo.trim(), ref.trim());
      setPreview(p);
      const seed: Record<string, string> = {};
      for (const f of p.setup) {
        if (f.type !== "display") seed[f.key] = "";
      }
      setCreds(seed);
    } catch (e) {
      setPreview(null);
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const doInstall = async () => {
    if (!token || !preview) return;
    setBusy(true);
    setErr(null);
    try {
      await api.installIntegrationFromGit(token, repo.trim(), ref.trim(), creds);
      setPreview(null);
      setRepo("");
      setRef("");
      setCreds({});
      onInstalled();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Section icon={<GitBranch size={16} />} title={t("admin.marketplace.installIntegration")}>
      <div className="mk-grid2">
        <label>
          <div className="sub">{t("admin.marketplace.repository")}</div>
          <input
            value={repo}
            placeholder="https://git.example.com/hazy/google"
            onChange={(e) => setRepo(e.target.value)}
            style={{ width: "100%", fontFamily: "var(--font-mono)" }}
          />
        </label>
        <label>
          <div className="sub">{t("admin.marketplace.tag")}</div>
          <input
            value={ref}
            placeholder="v1.0.0"
            onChange={(e) => setRef(e.target.value)}
            style={{ width: "100%", fontFamily: "var(--font-mono)" }}
          />
        </label>
      </div>
      <div style={{ marginTop: 8 }}>
        <button
          className="ghost"
          onClick={() => void doPreview()}
          disabled={busy || !repo.trim() || !ref.trim()}
        >
          {t("admin.marketplace.preview")}
        </button>
      </div>

      {preview && (
        <div className="sf-field" style={{ marginTop: 12 }}>
          <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <strong>{preview.label}</strong>
            <span className="sub">{preview.version}</span>
            <TierBadge tier={preview.tier} />
          </div>
          <div className="sub" style={{ margin: "4px 0" }}>{preview.summary}</div>
          {preview.commit && (
            <div className="sub" style={{ fontFamily: "var(--font-mono)" }}>
              {t("admin.marketplace.commit", { commit: preview.commit.slice(0, 12) })}
            </div>
          )}
          {preview.scopes && preview.scopes.length > 0 && (
            <div className="sub" style={{ marginTop: 6 }}>
              {t("admin.marketplace.scopes", { scopes: preview.scopes.join(", ") })}
            </div>
          )}

          <div style={{ marginTop: 10, display: "flex", flexDirection: "column", gap: 8 }}>
            {preview.setup.map((f) =>
              f.type === "display" ? (
                <label key={f.key}>
                  <div className="sub">{f.label}</div>
                  <input
                    readOnly
                    value={f.value ?? ""}
                    style={{ width: "100%", fontFamily: "var(--font-mono)" }}
                  />
                  {f.help && <div className="sub">{f.help}</div>}
                </label>
              ) : (
                <label key={f.key}>
                  <div className="sub">
                    {f.label}
                    {f.required ? " *" : ""}
                  </div>
                  <input
                    type={f.type === "secret" ? "password" : "text"}
                    value={creds[f.key] ?? ""}
                    onChange={(e) =>
                      setCreds((c) => ({ ...c, [f.key]: e.target.value }))
                    }
                    style={{ width: "100%", fontFamily: "var(--font-mono)" }}
                  />
                  {f.help && <div className="sub">{f.help}</div>}
                </label>
              ),
            )}
          </div>

          <div style={{ marginTop: 10 }}>
            <button className="primary" onClick={() => void doInstall()} disabled={busy}>
              <Download size={14} /> {t("admin.marketplace.install", { label: preview.label })}
            </button>
          </div>
        </div>
      )}

      {err && (
        <div className="sub" style={{ color: "var(--danger)", marginTop: 8 }}>
          {err}
        </div>
      )}
    </Section>
  );
}

// InstallDrop: paste repo+ref+path → Install. A 409 means the drop's
// integration prerequisite isn't installed yet.
function InstallDrop({
  token,
  onInstalled,
}: {
  token: string | null;
  onInstalled: () => void;
}) {
  const { t } = useTranslation();
  const [repo, setRepo] = useState("");
  const [ref, setRef] = useState("");
  const [path, setPath] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  // Two-step consent: preview the drop's requested access, then acknowledge it
  // before the install is allowed. A runtime-installed drop runs untrusted code.
  const [cap, setCap] = useState<DropCapabilitySummary | null>(null);
  const [acked, setAcked] = useState(false);

  const reset = () => {
    setRepo("");
    setRef("");
    setPath("");
    setCap(null);
    setAcked(false);
  };

  const canQuery = !!token && !!repo.trim() && !!ref.trim() && !!path.trim();

  const doPreview = async () => {
    if (!canQuery) return;
    setBusy(true);
    setErr(null);
    try {
      const c = await api.previewDropFromGit(token!, repo.trim(), ref.trim(), path.trim());
      setCap(c);
      setAcked(false);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const doInstall = async () => {
    if (!canQuery || !acked) return;
    setBusy(true);
    setErr(null);
    try {
      await api.installDropFromGit(token!, repo.trim(), ref.trim(), path.trim(), true);
      reset();
      onInstalled();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  // Editing the coordinates invalidates a stale preview.
  const onEdit = (set: (v: string) => void) => (v: string) => {
    set(v);
    if (cap) {
      setCap(null);
      setAcked(false);
    }
  };

  return (
    <Section icon={<GitBranch size={16} />} title={t("admin.marketplace.installDrop")}>
      <div className="mk-grid3">
        <label>
          <div className="sub">{t("admin.marketplace.repository")}</div>
          <input
            value={repo}
            placeholder="https://git.example.com/hazy/google"
            onChange={(e) => onEdit(setRepo)(e.target.value)}
            style={{ width: "100%", fontFamily: "var(--font-mono)" }}
          />
        </label>
        <label>
          <div className="sub">{t("admin.marketplace.tag")}</div>
          <input
            value={ref}
            placeholder="v1.0.0"
            onChange={(e) => onEdit(setRef)(e.target.value)}
            style={{ width: "100%", fontFamily: "var(--font-mono)" }}
          />
        </label>
        <label>
          <div className="sub">{t("admin.marketplace.path")}</div>
          <input
            value={path}
            placeholder="gmail_send.ts"
            onChange={(e) => onEdit(setPath)(e.target.value)}
            style={{ width: "100%", fontFamily: "var(--font-mono)" }}
          />
        </label>
      </div>

      {!cap && (
        <div style={{ marginTop: 8 }}>
          <button className="primary" onClick={() => void doPreview()} disabled={busy || !canQuery}>
            <Eye size={14} /> {t("admin.marketplace.reviewDropBtn")}
          </button>
        </div>
      )}

      {cap && (
        <DropConsent
          cap={cap}
          acked={acked}
          busy={busy}
          onAck={setAcked}
          onInstall={() => void doInstall()}
          onCancel={reset}
        />
      )}

      {err && (
        <div className="sub" style={{ color: "var(--danger)", marginTop: 8 }}>
          {err}
        </div>
      )}
    </Section>
  );
}

// DropConsent shows the access a drop declares and gates install on an explicit
// acknowledgement — the human trust decision for untrusted, sandboxed code.
function DropConsent({
  cap,
  acked,
  busy,
  onAck,
  onInstall,
  onCancel,
}: {
  cap: DropCapabilitySummary;
  acked: boolean;
  busy: boolean;
  onAck: (v: boolean) => void;
  onInstall: () => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const none = t("admin.marketplace.capNone");
  return (
    <div className="mk-preview" style={{ marginTop: 12 }}>
      <div className="mk-preview-head">
        <ShieldAlert size={16} />
        <strong>{cap.label}</strong>
        <span className="sub">{cap.version}</span>
        <TierBadge tier={cap.tier} />
      </div>
      <div className="sub" style={{ margin: "4px 0" }}>{cap.summary}</div>
      {cap.commit && (
        <div className="sub">{t("admin.marketplace.commit", { commit: cap.commit.slice(0, 12) })}</div>
      )}

      <dl className="mk-caps" style={{ margin: "8px 0" }}>
        <dt>{t("admin.marketplace.capSandbox")}</dt>
        <dd>{t("admin.marketplace.capSandboxVal")}</dd>
        <dt>{t("admin.marketplace.capOAuth")}</dt>
        <dd>{cap.oauth.length ? cap.oauth.join(", ") : none}</dd>
        <dt>{t("admin.marketplace.capSecrets")}</dt>
        <dd>{cap.secrets.length ? cap.secrets.join(", ") : none}</dd>
        <dt>{t("admin.marketplace.capEgress")}</dt>
        <dd>{cap.egress.length ? cap.egress.join(", ") : t("admin.marketplace.capNoEgress")}</dd>
      </dl>

      <label style={{ display: "flex", gap: 8, alignItems: "flex-start" }}>
        <input type="checkbox" checked={acked} onChange={(e) => onAck(e.target.checked)} />
        <span className="sub">{t("admin.marketplace.capAck")}</span>
      </label>

      <div style={{ marginTop: 8, display: "flex", gap: 8 }}>
        <button className="primary" onClick={onInstall} disabled={busy || !acked}>
          <Download size={14} /> {t("admin.marketplace.installDropBtn")}
        </button>
        <button onClick={onCancel} disabled={busy}>
          {t("admin.marketplace.cancel")}
        </button>
      </div>
    </div>
  );
}

function Section({
  icon,
  title,
  children,
}: {
  icon: ReactNode;
  title: string;
  children: ReactNode;
}) {
  return (
    <div className="card" style={{ marginBottom: 12 }}>
      <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 8 }}>
        {icon}
        <strong>{title}</strong>
      </div>
      {children}
    </div>
  );
}

function TierBadge({ tier }: { tier: TrustTier }) {
  const color =
    tier === "official"
      ? "var(--accent)"
      : tier === "verified"
        ? "#5599ee"
        : "var(--muted)";
  return (
    <span
      style={{
        display: "inline-block",
        padding: "2px 8px",
        borderRadius: "var(--r-pill)",
        fontSize: 11,
        fontWeight: 500,
        color,
        background: `color-mix(in srgb, ${color} 18%, transparent)`,
      }}
    >
      {tier}
    </span>
  );
}
