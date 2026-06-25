import { useCallback, useEffect, useMemo, useState } from "react";
import { AlertCircle, Power, PowerOff, Search, ShieldCheck } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, type PlatformDrop } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { Button } from "../components/Button";
import { ConfirmModal } from "../components/ConfirmModal";

// AdminPlatformDrops is the platform-operator killswitch for individual
// drops. A drop switched off here is refused by the engine on every run
// (globally, for every org) and hidden from the build palette — the
// abuse-response lever for "this integration is being misused". Per-org
// switches exist in the API too; this page drives the global switch,
// which is the blunt instrument an operator reaches for first.
export function AdminPlatformDrops() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const [drops, setDrops] = useState<PlatformDrop[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<PlatformDrop | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.platformListDrops(token);
      setDrops(r.drops ?? []);
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

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return drops;
    return drops.filter(
      (d) =>
        d.id.toLowerCase().includes(q) ||
        d.label.toLowerCase().includes(q) ||
        (d.integration ?? "").toLowerCase().includes(q),
    );
  }, [drops, query]);

  const disabledCount = drops.filter((d) => d.globally_disabled).length;

  const toggle = async (d: PlatformDrop, reason: string) => {
    if (!token) return;
    setBusy(d.id);
    setError(null);
    try {
      if (d.globally_disabled) {
        await api.platformEnableDrop(token, d.id);
      } else {
        await api.platformDisableDrop(token, d.id, reason);
      }
      await refresh();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(null);
    }
  };

  if (!hasPerm("platform:admin")) {
    return (
      <div className="card" style={{ color: "var(--danger)" }}>
        <Trans i18nKey="admin.platform.needPlatformAdmin" components={[<code />]} />
      </div>
    );
  }

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <ShieldCheck size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("admin.platformDrops.title")}
          </h1>
          <div className="sub">{t("admin.platformDrops.subtitle")}</div>
        </div>
      </div>

      {error && (
        <div className="card error" style={{ marginBottom: "var(--space-3)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}

      <div className="sub" style={{ marginBottom: "var(--space-2)" }}>
        {t("admin.platformDrops.disabledCount", { count: disabledCount })}
      </div>

      <div style={{ position: "relative", marginBottom: "var(--space-3)" }}>
        <Search
          size={15}
          style={{ position: "absolute", left: 10, top: 11, color: "var(--muted)" }}
        />
        <input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={t("admin.platformDrops.searchPlaceholder")}
          aria-label={t("admin.platformDrops.searchPlaceholder")}
          style={{ width: "100%", paddingLeft: 32 }}
        />
      </div>

      {loading ? (
        <div className="card" style={{ color: "var(--muted)" }}>
          {t("common.loading")}
        </div>
      ) : (
        <div className="user-list">
          {filtered.map((d) => (
            <div className="user-card" key={d.id}>
              <div style={{ minWidth: 0 }}>
                <div className="subject">
                  {d.label || d.id}
                  {d.globally_disabled && (
                    <span
                      className="count-pill"
                      style={{ marginLeft: 8, color: "var(--danger)" }}
                    >
                      {t("admin.platformDrops.off")}
                    </span>
                  )}
                  {(d.disabled_tenants?.length ?? 0) > 0 && (
                    <span className="count-pill" style={{ marginLeft: 8 }}>
                      {t("admin.platformDrops.perOrgCount", {
                        count: d.disabled_tenants?.length ?? 0,
                      })}
                    </span>
                  )}
                </div>
                <div className="meta">
                  <code>{d.id}</code>
                  {d.integration ? ` · ${d.integration}` : ""}
                  {d.globally_disabled && d.reason ? ` · ${d.reason}` : ""}
                </div>
              </div>
              <div className="user-card-actions">
                {d.globally_disabled ? (
                  <Button
                    variant="primary"
                    disabled={busy === d.id}
                    onClick={() => void toggle(d, "")}
                  >
                    <Power size={13} style={{ marginRight: 4 }} />
                    {t("admin.platformDrops.enable")}
                  </Button>
                ) : (
                  <Button
                    variant="danger"
                    disabled={busy === d.id}
                    onClick={() => setConfirm(d)}
                  >
                    <PowerOff size={13} style={{ marginRight: 4 }} />
                    {t("admin.platformDrops.disable")}
                  </Button>
                )}
              </div>
            </div>
          ))}
          {filtered.length === 0 && (
            <div className="card" style={{ color: "var(--muted)" }}>
              {t("admin.platformDrops.none")}
            </div>
          )}
        </div>
      )}

      {confirm && (
        <DisableDropModal
          drop={confirm}
          onConfirm={(reason) => {
            const d = confirm;
            setConfirm(null);
            void toggle(d, reason);
          }}
          onCancel={() => setConfirm(null)}
        />
      )}
    </div>
  );
}

// DisableDropModal collects the operator's reason (audit trail) before
// the global killswitch fires.
function DisableDropModal({
  drop,
  onConfirm,
  onCancel,
}: {
  drop: PlatformDrop;
  onConfirm: (reason: string) => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const [reason, setReason] = useState("");
  return (
    <ConfirmModal
      title={t("admin.platformDrops.disableTitle", { drop: drop.label || drop.id })}
      message={
        <div>
          <p>{t("admin.platformDrops.disableWarning")}</p>
          <input
            type="text"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder={t("admin.platformDrops.reasonPlaceholder")}
            aria-label={t("admin.platformDrops.reasonPlaceholder")}
            style={{ width: "100%", marginTop: "var(--space-2)" }}
            autoFocus
          />
        </div>
      }
      confirmLabel={t("admin.platformDrops.disable")}
      danger
      onConfirm={() => onConfirm(reason.trim())}
      onCancel={onCancel}
    />
  );
}
