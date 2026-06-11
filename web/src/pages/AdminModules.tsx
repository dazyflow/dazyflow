import { useCallback, useEffect, useMemo, useState } from "react";
import { AlertCircle, Boxes } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import type { Manifest } from "../types";

// AdminModules is a read-only operability view of the drops registered on
// this daemon — native, remote, and MCP — grouped by category, so an
// admin can see what's installed without opening the flow editor.
export function AdminModules() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const [drops, setDrops] = useState<Manifest[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.listDrops(token);
      setDrops(r.drops ?? []);
      setError(null);
    } catch (e) {
      setError((e as APIError | Error).message);
    } finally {
      setLoading(false);
    }
  }, [token]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // Group by category, categories alphabetical, modules by id within each.
  const groups = useMemo(() => {
    const byCat = new Map<string, Manifest[]>();
    for (const m of drops) {
      const cat = m.category || "other";
      (byCat.get(cat) ?? byCat.set(cat, []).get(cat)!).push(m);
    }
    return [...byCat.entries()]
      .sort((a, b) => a[0].localeCompare(b[0]))
      .map(([cat, ms]) => [cat, ms.sort((a, b) => a.id.localeCompare(b.id))] as const);
  }, [drops]);

  if (!hasPerm("organization:admin")) {
    return (
      <div className="card" style={{ color: "var(--danger)" }}>
        <Trans i18nKey="admin.modules.needAdmin" components={[<code />]} />
      </div>
    );
  }

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <Boxes size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("admin.modules.title")}
          </h1>
          <div className="sub">
            {t("admin.modules.subtitle")}
            {drops.length > 0 && <> · {t("admin.modules.count", { count: drops.length })}</>}
          </div>
        </div>
      </div>

      {error && (
        <div className="card" style={{ color: "var(--danger)", marginBottom: "var(--space-4)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}

      {loading && drops.length === 0 && (
        <div className="card" style={{ color: "var(--muted)" }}>{t("common.loading")}</div>
      )}
      {!loading && drops.length === 0 && !error && (
        <div className="card" style={{ color: "var(--muted)" }}>{t("admin.modules.empty")}</div>
      )}

      {groups.map(([cat, ms]) => (
        <div key={cat} style={{ marginBottom: "var(--space-4)" }}>
          <h3 style={{ textTransform: "capitalize", color: "var(--muted)", margin: "0 0 var(--space-2)" }}>{cat}</h3>
          <div className="card" style={{ padding: 0 }}>
            {ms.map((m) => (
              <div
                key={m.id}
                style={{ padding: "10px 14px", borderTop: "1px solid var(--border)" }}
              >
                <div style={{ display: "flex", gap: 8, alignItems: "baseline", flexWrap: "wrap" }}>
                  <strong>{m.label || m.id}</strong>
                  <code style={{ fontSize: "var(--text-sm)", color: "var(--muted)" }}>{m.id}</code>
                  {m.provider && <span className="perm-chip">{m.provider}</span>}
                  {m.integration && <span className="perm-chip">{m.integration}</span>}
                  {m.idempotent && <span className="perm-chip">{t("admin.modules.idempotent")}</span>}
                  {m.awaits_approval && <span className="perm-chip">{t("admin.modules.awaitsApproval")}</span>}
                  {m.submits_child_graph && <span className="perm-chip">{t("admin.modules.submitsChild")}</span>}
                </div>
                {m.description && (
                  <div style={{ fontSize: "var(--text-md)", color: "var(--muted)", marginTop: 4 }}>{m.description}</div>
                )}
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}
