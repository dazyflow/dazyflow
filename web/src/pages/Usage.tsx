import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Activity, AlertCircle, CreditCard, Layers, Sparkles } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "../components/Button";
import { PlanComparison } from "../components/PlanComparison";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import type { PlansInfo } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { formatDate } from "../lib/datetime";
import type { BillingInfo, UsageCounters } from "../types";

// Plan & usage (T3): the single account-billing surface, styled after the
// Overview dashboard — a row of at-a-glance stat cards (plan, runs, step
// executions), then the monthly history panel and the plan-comparison grid.
// The Stripe Checkout / billing-portal redirects live in the title actions.
// The old standalone /plans page folded in here; /plans now redirects in.

// periodLabel renders the month bucket as a standard "YYYY-MM".
function periodLabel(period: string): string {
  const [y, m] = period.split("-").map(Number);
  if (!y || !m) return period;
  return `${y}-${String(m).padStart(2, "0")}`;
}

export function Usage() {
  const { t, i18n } = useTranslation();
  const { token, activeTenant } = useAuth();
  const [usage, setUsage] = useState<UsageCounters[]>([]);
  const [billing, setBilling] = useState<BillingInfo | null>(null);
  const [plans, setPlans] = useState<PlansInfo | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [redirecting, setRedirecting] = useState(false);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const tenant = activeTenant || undefined;
      const [r, b, p] = await Promise.all([
        api.getUsage(token, { tenant, months: 12 }),
        // Billing state and the plan comparison are optional decoration —
        // a deployment without a plan store still renders the counters.
        api.getBilling(token, tenant).catch(() => null),
        api.getPlans(token, tenant).catch(() => null),
      ]);
      setUsage(r.usage ?? []);
      setBilling(b);
      setPlans(p);
      setError(null);
    } catch (e) {
      const err = e as APIError | Error;
      if (err instanceof APIError && err.status === 501) {
        setError(t("usage.notConfigured"));
      } else {
        setError(explainApiError(e, t));
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
        setError(explainApiError(e, t));
        setRedirecting(false);
        // 409 = already subscribed (stale page / double-submit). Re-sync so
        // the Upgrade button disappears and the real plan state shows.
        if (e instanceof APIError && e.status === 409) {
          void refresh();
        }
      }
    },
    [token, activeTenant, t, refresh],
  );

  const current = usage[0];
  const fmt = new Intl.NumberFormat(i18n.language);

  // Free-tier run-cap proximity drives the runs-card tone, the way the
  // Dashboard's success-rate / failure cards colour themselves.
  const capped =
    billing?.plan === "free" && billing.free_runs_per_month > 0
      ? billing.free_runs_per_month
      : 0;
  const usedRatio = capped ? billing!.runs_this_month / capped : 0;
  const runsTone: StatTone = capped
    ? usedRatio >= 1
      ? "bad"
      : usedRatio >= 0.8
        ? "warn"
        : "neutral"
    : "neutral";
  // Scheduled fires the run cap refused this month — otherwise an invisible
  // log-only event. Drives the "N runs skipped" banner.
  const skippedThisMonth = current?.skipped_runs ?? 0;

  // Plan card sub + tone. A subscription set to cancel at period end stays
  // "active" in Stripe, so we surface the end date as a warn chip rather than
  // letting it read as an open-ended plan; an active sub shows its renewal
  // date; a free tenant shows runs left against the cap.
  let planSub: React.ReactNode;
  let planTone: StatTone = "neutral";
  if (billing?.plan === "pro") {
    planTone = "good";
    if (billing.cancel_at_period_end && billing.current_period_end) {
      planTone = "warn";
      planSub = (
        <span className="badge warn">
          {t("usage.cancelsOn", { date: formatDate(billing.current_period_end) })}
        </span>
      );
    } else if (billing.current_period_end) {
      planSub = t("usage.renewsOn", { date: formatDate(billing.current_period_end) });
    } else {
      planSub = billing.subscription_status || undefined;
    }
  } else if (capped) {
    planSub = t("usage.runsLeft", { n: Math.max(0, capped - billing!.runs_this_month) });
  }

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <CreditCard size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("usage.title")}
          </h1>
          <div className="sub">{t("usage.subtitle")}</div>
        </div>
        {billing && (billing.can_upgrade || billing.can_manage) && (
          <div className="dash-title-actions">
            {billing.can_upgrade && (
              <Button variant="primary" disabled={redirecting} onClick={() => void goToStripe("checkout")}>
                <Sparkles size={16} style={{ marginRight: 6 }} />
                {t("usage.upgrade")}
              </Button>
            )}
            {billing.can_manage && (
              <Button variant="ghost" disabled={redirecting} onClick={() => void goToStripe("portal")}>
                {t("usage.manageBilling")}
              </Button>
            )}
          </div>
        )}
      </div>

      {error && (
        <div className="card" style={{ color: "var(--danger)", marginBottom: "var(--space-4)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}

      {!loading && !error && (
        <div className="dash-stats">
          {billing && (
            <StatCard
              icon={<CreditCard size={18} />}
              label={t("usage.planLabel")}
              value={billing.plan === "pro" ? t("usage.planValuePro") : t("usage.planValueFree")}
              sub={planSub}
              tone={planTone}
            />
          )}
          <StatCard
            icon={<Activity size={18} />}
            label={t("usage.runs")}
            value={loading ? "—" : fmt.format(current?.graph_runs ?? 0)}
            sub={
              capped
                ? t("usage.runsUsed", {
                    used: billing!.runs_this_month,
                    limit: capped,
                  })
                : current
                  ? t("usage.currentMonth", { month: periodLabel(current.period) })
                  : undefined
            }
            tone={runsTone}
            to="/runs"
          />
          <StatCard
            icon={<Layers size={18} />}
            label={t("usage.nodeExecutions")}
            value={loading ? "—" : fmt.format(current?.node_executions ?? 0)}
            to="/runs"
          />
        </div>
      )}

      {/* Plan-state notes that don't fit a stat card: scheduled runs the cap
          skipped this month, paused triggers on a gated free tier, and the
          pitch to upgrade. */}
      {!loading &&
        !error &&
        billing &&
        (skippedThisMonth > 0 || !billing.polling_allowed || billing.can_upgrade) && (
          <div className="card dash-panel" style={{ marginBottom: "var(--space-4)" }}>
            {skippedThisMonth > 0 && (
              <p className="dash-empty" style={{ color: "var(--warning, #d97706)" }}>
                <AlertCircle size={16} />
                {t("usage.skippedRuns", { count: skippedThisMonth })}
              </p>
            )}
            {!billing.polling_allowed && (
              <p className="dash-empty" style={{ color: "var(--warning, #d97706)" }}>
                <AlertCircle size={16} />
                {t("usage.pollingGated")}
              </p>
            )}
            {billing.can_upgrade && (
              <p className="dash-empty">
                <Sparkles size={16} />
                {t("usage.proPitch")}
              </p>
            )}
          </div>
        )}

      {!loading && !error && usage.length > 1 && (
        <section className="card dash-panel">
          <div className="dash-panel-head">
            <h2>{t("usage.historyTitle")}</h2>
          </div>
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
                  <td>{periodLabel(u.period)}</td>
                  <td style={{ textAlign: "right" }}>{fmt.format(u.graph_runs)}</td>
                  <td style={{ textAlign: "right" }}>{fmt.format(u.node_executions)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      )}

      {/* Plan comparison — the old /plans grid, inline below usage. */}
      {!loading && !error && plans && (
        <PlanComparison
          info={plans}
          redirecting={redirecting}
          onUpgrade={() => void goToStripe("checkout")}
          onManage={() => void goToStripe("portal")}
        />
      )}
    </div>
  );
}

type StatTone = "neutral" | "good" | "warn" | "bad";

function StatCard({
  icon,
  label,
  value,
  sub,
  tone = "neutral",
  to,
}: {
  icon: React.ReactNode;
  label: string;
  value: string;
  sub?: React.ReactNode;
  tone?: StatTone;
  to?: string;
}) {
  const className = "card dash-stat dash-stat-" + tone;
  const body = (
    <>
      <span className="dash-stat-icon">{icon}</span>
      <span className="dash-stat-value">{value}</span>
      <span className="dash-stat-label">{label}</span>
      {sub && <span className="dash-stat-sub">{sub}</span>}
    </>
  );
  return to ? (
    <Link to={to} className={className}>
      {body}
    </Link>
  ) : (
    <div className={className}>{body}</div>
  );
}
