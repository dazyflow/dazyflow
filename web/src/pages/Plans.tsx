import { useCallback, useEffect, useState } from "react";
import {
  AlertCircle,
  ArrowUp,
  Check,
  CreditCard,
  Minus,
  Sparkles,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "../components/Button";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import { explainApiError } from "../lib/explainApiError";
import type { PlanLimits, PlanOption, PlansInfo } from "../api";

// Plans is the data-driven plan-comparison / change-tier page. Each plan's
// limits are resolved server-side (the same resolver the gates use), so this
// page only formats and *diffs* the numbers against the current plan — there is
// no per-tier marketing copy to keep in sync. Editing a tier's limits changes
// what's shown here automatically; only a brand-new limit field needs a new
// row in FEATURES below.

type FeatureKind = "capacity" | "bytes" | "duration" | "bool";

type Feature = {
  key: keyof PlanLimits;
  labelKey: string;
  kind: FeatureKind;
};

// The one place limit fields are described. Order = display order. Capacity /
// bytes / duration all treat 0 as "Unlimited" (best); bool treats true as best.
const FEATURES: Feature[] = [
  { key: "runs_per_month", labelKey: "plans.feat.runs", kind: "capacity" },
  { key: "polling_allowed", labelKey: "plans.feat.polling", kind: "bool" },
  { key: "max_flows", labelKey: "plans.feat.flows", kind: "capacity" },
  { key: "max_graph_nodes", labelKey: "plans.feat.nodes", kind: "capacity" },
  { key: "disk_quota_bytes", labelKey: "plans.feat.disk", kind: "bytes" },
  { key: "max_timeout_seconds", labelKey: "plans.feat.timeout", kind: "duration" },
];

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

// score maps a numeric limit to a comparable magnitude where 0 = unlimited is
// the largest. Used to decide whether a plan's value is an upgrade over the
// current one, regardless of the specific numbers a tier carries.
function score(v: number): number {
  return v === 0 ? Infinity : v;
}

// betterThan reports whether plan value `a` is an improvement over current `b`.
function betterThan(f: Feature, a: number | boolean, b: number | boolean): boolean {
  if (f.kind === "bool") return a === true && b !== true;
  return score(a as number) > score(b as number);
}

export function Plans() {
  const { t, i18n } = useTranslation();
  const { token, activeTenant } = useAuth();
  const [info, setInfo] = useState<PlansInfo | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [redirecting, setRedirecting] = useState(false);
  const fmt = new Intl.NumberFormat(i18n.language);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const data = await api.getPlans(token, activeTenant || undefined);
      setInfo(data);
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
      }
    },
    [token, activeTenant, t],
  );

  const formatValue = (f: Feature, v: number | boolean): string => {
    switch (f.kind) {
      case "bool":
        return v ? t("plans.included") : t("plans.notIncluded");
      case "bytes":
        return v === 0 ? t("plans.unlimited") : formatBytes(v as number);
      case "duration": {
        const s = v as number;
        if (s === 0) return t("plans.unlimited");
        return s % 60 === 0
          ? t("plans.minutes", { n: s / 60 })
          : t("plans.seconds", { n: fmt.format(s) });
      }
      case "capacity":
      default: {
        const n = v as number;
        if (n === 0) return t("plans.unlimited");
        return f.key === "runs_per_month"
          ? t("plans.perMonth", { n: fmt.format(n) })
          : fmt.format(n);
      }
    }
  };

  if (!loading && error) {
    return (
      <div>
        <PlansHeader t={t} />
        <div className="card" style={{ color: "var(--danger)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      </div>
    );
  }

  const plans = info?.plans ?? [];
  const current = plans.find((p) => p.is_current) ?? plans[0];

  return (
    <div>
      <PlansHeader t={t} />

      {!loading && info && current && (
        <>
          <div className="sub" style={{ marginBottom: "var(--space-4)" }}>
            {t("plans.currentLine", {
              plan: current.name,
              used: fmt.format(info.runs_this_month),
            })}
          </div>

          <div className="plans-grid">
            {plans.map((p) => (
              <PlanCard
                key={p.id}
                plan={p}
                current={current}
                info={info}
                redirecting={redirecting}
                t={t}
                betterThan={betterThan}
                formatValue={formatValue}
                onUpgrade={() => void goToStripe("checkout")}
                onManage={() => void goToStripe("portal")}
              />
            ))}
          </div>
        </>
      )}
    </div>
  );
}

function PlansHeader({ t }: { t: (k: string) => string }) {
  return (
    <div className="page-title">
      <div>
        <h1>
          <CreditCard size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
          {t("plans.title")}
        </h1>
        <div className="sub">{t("plans.subtitle")}</div>
      </div>
    </div>
  );
}

function PlanCard({
  plan,
  current,
  info,
  redirecting,
  t,
  betterThan,
  formatValue,
  onUpgrade,
  onManage,
}: {
  plan: PlanOption;
  current: PlanOption;
  info: PlansInfo;
  redirecting: boolean;
  t: (k: string, o?: Record<string, unknown>) => string;
  betterThan: (f: Feature, a: number | boolean, b: number | boolean) => boolean;
  formatValue: (f: Feature, v: number | boolean) => string;
  onUpgrade: () => void;
  onManage: () => void;
}) {
  const isCurrent = plan.is_current;
  // An upgrade target: a higher (pro) plan the org isn't already on. Stripe
  // self-serve only handles Pro; custom comp tiers are admin-assigned.
  const isUpgradeTarget =
    !isCurrent && plan.plan === "pro" && current.plan !== "pro";

  return (
    <div className={"card plan-card" + (isCurrent ? " plan-card-current" : "")}>
      <div className="plan-card-head">
        <h2>{plan.name}</h2>
        {isCurrent && <span className="plan-badge">{t("plans.yourPlan")}</span>}
      </div>

      <ul className="plan-feats">
        {FEATURES.map((f) => {
          const v = plan.limits[f.key];
          const cur = current.limits[f.key];
          const up = !isCurrent && betterThan(f, v, cur);
          return (
            <li
              key={f.key}
              className={"plan-feat" + (up ? " plan-feat-up" : "")}
            >
              <span className="plan-feat-ico">
                {f.kind === "bool" ? (
                  v ? (
                    <Check size={15} />
                  ) : (
                    <Minus size={15} />
                  )
                ) : up ? (
                  <ArrowUp size={15} />
                ) : (
                  <Check size={15} />
                )}
              </span>
              <span className="plan-feat-label">{t(f.labelKey)}</span>
              <span className="plan-feat-val">{formatValue(f, v)}</span>
            </li>
          );
        })}
      </ul>

      <div className="plan-card-foot">
        {isCurrent ? (
          info.can_manage ? (
            <Button variant="ghost" disabled={redirecting} onClick={onManage}>
              {t("plans.manageBilling")}
            </Button>
          ) : (
            <Button variant="ghost" disabled>
              {t("plans.currentPlan")}
            </Button>
          )
        ) : isUpgradeTarget ? (
          info.can_upgrade ? (
            <Button variant="primary" disabled={redirecting} onClick={onUpgrade}>
              <Sparkles size={14} style={{ marginRight: 6 }} />
              {t("plans.upgradeTo", { plan: plan.name })}
            </Button>
          ) : (
            <div className="sub plan-contact">{t("plans.contactAdmin")}</div>
          )
        ) : null}
      </div>
    </div>
  );
}
