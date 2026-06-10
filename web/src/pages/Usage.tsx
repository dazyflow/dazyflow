import { useCallback, useEffect, useState } from "react";
import { AlertCircle, Gauge, Sparkles } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import type { BillingInfo, UsageCounters } from "../types";

// Usage & billing (T3): how many flow runs and step executions this
// organization consumed by month, plus the plan card — current plan,
// progress against the free-tier cap when the deployment enforces one,
// and the Stripe Checkout / billing-portal redirects. Counters are
// maintained server-side on every run submission / node execution.

// periodLabel renders "2026-06" in the viewer's locale ("June 2026")
// so the table doesn't read like raw database keys.
function periodLabel(period: string, locale: string): string {
  const [y, m] = period.split("-").map(Number);
  if (!y || !m) return period;
  return new Date(Date.UTC(y, m - 1, 1)).toLocaleDateString(locale, {
    month: "long",
    year: "numeric",
    timeZone: "UTC",
  });
}

export function Usage() {
  const { t, i18n } = useTranslation();
  const { token, activeTenant } = useAuth();
  const [usage, setUsage] = useState<UsageCounters[]>([]);
  const [billing, setBilling] = useState<BillingInfo | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [redirecting, setRedirecting] = useState(false);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const tenant = activeTenant || undefined;
      const [r, b] = await Promise.all([
        api.getUsage(token, { tenant, months: 12 }),
        // Billing state is optional decoration — a deployment without
        // a plan store still renders the counters.
        api.getBilling(token, tenant).catch(() => null),
      ]);
      setUsage(r.usage ?? []);
      setBilling(b);
      setError(null);
    } catch (e) {
      const err = e as APIError | Error;
      if (err instanceof APIError && err.status === 501) {
        setError(t("usage.notConfigured"));
      } else {
        setError(err.message);
      }
    } finally {
      setLoading(false);
    }
  }, [token, activeTenant, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // Upgrade / manage both round-trip through the daemon for a Stripe
  // URL, then leave the app. Errors land in the shared banner.
  const goToStripe = useCallback(
    async (kind: "checkout" | "portal") => {
      if (!token) return;
      setRedirecting(true);
      try {
        const tenant = activeTenant || undefined;
        const { url } =
          kind === "checkout"
            ? await api.createCheckout(token, tenant)
            : await api.createBillingPortal(token, tenant);
        window.location.href = url;
      } catch (e) {
        setError((e as Error).message);
        setRedirecting(false);
      }
    },
    [token, activeTenant],
  );

  const current = usage[0];
  const fmt = new Intl.NumberFormat(i18n.language);

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <Gauge size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("usage.title")}
          </h1>
          <div className="sub">{t("usage.subtitle")}</div>
        </div>
      </div>

      {error && (
        <div className="card" style={{ color: "var(--danger)", marginBottom: "var(--space-4)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}

      {!loading && !error && billing && (
        <div className="card" style={{ marginBottom: "var(--space-4)" }}>
          <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", flexWrap: "wrap", gap: "var(--space-3)" }}>
            <div>
              <h2 style={{ marginTop: 0, marginBottom: 4 }}>
                {billing.plan === "pro" ? t("usage.planPro") : t("usage.planFree")}
              </h2>
              {billing.plan === "free" && billing.free_runs_per_month > 0 && (
                <div className="sub">
                  {t("usage.runsUsed", {
                    used: billing.runs_this_month,
                    limit: billing.free_runs_per_month,
                  })}
                </div>
              )}
              {billing.plan === "pro" && billing.subscription_status && billing.subscription_status !== "active" && (
                <div className="sub" style={{ color: "var(--warning, #d97706)" }}>
                  {t("usage.subscriptionStatus", { status: billing.subscription_status })}
                </div>
              )}
            </div>
            <div style={{ display: "flex", gap: 8 }}>
              {billing.can_upgrade && (
                <button className="primary" disabled={redirecting} onClick={() => void goToStripe("checkout")}>
                  <Sparkles size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
                  {t("usage.upgrade")}
                </button>
              )}
              {billing.can_manage && (
                <button className="ghost" disabled={redirecting} onClick={() => void goToStripe("portal")}>
                  {t("usage.manageBilling")}
                </button>
              )}
            </div>
          </div>
          {/* Free-tier progress bar — turns to the danger colour at the cap
              so "why won't my flow run" answers itself. */}
          {billing.plan === "free" && billing.free_runs_per_month > 0 && (
            <div
              style={{
                marginTop: "var(--space-3)",
                height: 8,
                borderRadius: 4,
                background: "var(--border)",
                overflow: "hidden",
              }}
            >
              <div
                style={{
                  height: "100%",
                  width: `${Math.min(100, (billing.runs_this_month / billing.free_runs_per_month) * 100)}%`,
                  background:
                    billing.runs_this_month >= billing.free_runs_per_month
                      ? "var(--danger)"
                      : "var(--accent, #6366f1)",
                }}
              />
            </div>
          )}
        </div>
      )}

      {!loading && !error && current && (
        <>
          <div className="card" style={{ marginBottom: "var(--space-4)" }}>
            <h2 style={{ marginTop: 0 }}>
              {t("usage.currentMonth", {
                month: periodLabel(current.period, i18n.language),
              })}
            </h2>
            <div style={{ display: "flex", gap: "var(--space-6)", flexWrap: "wrap" }}>
              <div>
                <div style={{ fontSize: 32, fontWeight: 700 }}>
                  {fmt.format(current.graph_runs)}
                </div>
                <div className="sub">{t("usage.runs")}</div>
              </div>
              <div>
                <div style={{ fontSize: 32, fontWeight: 700 }}>
                  {fmt.format(current.node_executions)}
                </div>
                <div className="sub">{t("usage.nodeExecutions")}</div>
              </div>
            </div>
          </div>

          {usage.length > 1 && (
            <div className="card">
              <h2 style={{ marginTop: 0 }}>{t("usage.historyTitle")}</h2>
              <table className="run-table">
                <thead>
                  <tr>
                    <th>{t("usage.month")}</th>
                    <th style={{ textAlign: "right" }}>{t("usage.runs")}</th>
                    <th style={{ textAlign: "right" }}>{t("usage.nodeExecutions")}</th>
                  </tr>
                </thead>
                <tbody>
                  {usage.map((u) => (
                    <tr key={u.period}>
                      <td>{periodLabel(u.period, i18n.language)}</td>
                      <td style={{ textAlign: "right" }}>{fmt.format(u.graph_runs)}</td>
                      <td style={{ textAlign: "right" }}>{fmt.format(u.node_executions)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </div>
  );
}
