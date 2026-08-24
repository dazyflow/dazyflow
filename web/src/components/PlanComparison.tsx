// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from "react";
import { ArrowUp, Check, CreditCard, Minus, Sparkles } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button, ButtonLink } from "./Button";
import type { PlanLimits, PlanOption, PlansInfo } from "../api";
import { ICON } from "../icons";
import { formatBytes } from "../lib/format";

// Unlimited renders the ∞ glyph for a no-limit value, keeping the localized
// "Unlimited" as the accessible label (tooltip + screen reader) so the symbol
// stays clear and translatable.
function Unlimited({ label }: { label: string }) {
  return (
    <span className="plan-unlimited" title={label} aria-label={label}>
      ∞
    </span>
  );
}

// The plan-comparison grid, lifted out of the old Plans page so the merged
// Plan & usage page can render it inline below the usage counters. Limits are
// resolved server-side (the same resolver the gates use), so this only formats
// and *diffs* the numbers against the current plan — there is no per-tier
// marketing copy to keep in sync. Editing a tier's limits changes what's shown
// here automatically; only a brand-new limit field needs a new row in FEATURES.

type FeatureKind = "capacity" | "bytes" | "duration" | "days" | "bool";

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
  { key: "max_members", labelKey: "plans.feat.members", kind: "capacity" },
  { key: "max_concurrency", labelKey: "plans.feat.concurrency", kind: "capacity" },
  { key: "retention_days", labelKey: "plans.feat.retention", kind: "days" },
  { key: "max_flows", labelKey: "plans.feat.flows", kind: "capacity" },
  { key: "max_graph_nodes", labelKey: "plans.feat.nodes", kind: "capacity" },
  { key: "disk_quota_bytes", labelKey: "plans.feat.disk", kind: "bytes" },
  { key: "max_timeout_seconds", labelKey: "plans.feat.timeout", kind: "duration" },
];


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

export function PlanComparison({
  info,
  redirecting,
  onUpgrade,
  onManage,
}: {
  info: PlansInfo;
  redirecting: boolean;
  onUpgrade: () => void;
  onManage: () => void;
}) {
  const { t, i18n } = useTranslation();
  const fmt = new Intl.NumberFormat(i18n.language);

  const plans = info.plans ?? [];
  const current = plans.find((p) => p.is_current) ?? plans[0];
  if (!current) return null;

  const unlimited = <Unlimited label={t("plans.unlimited")} />;
  const formatValue = (f: Feature, v: number | boolean): ReactNode => {
    switch (f.kind) {
      case "bool":
        return v ? t("plans.included") : t("plans.notIncluded");
      case "bytes":
        return v === 0 ? unlimited : formatBytes(v as number);
      case "duration": {
        const s = v as number;
        if (s === 0) return unlimited;
        return s % 60 === 0
          ? t("plans.minutes", { n: s / 60 })
          : t("plans.seconds", { n: fmt.format(s) });
      }
      case "days": {
        const d = v as number;
        return d === 0 ? unlimited : t("plans.days", { n: fmt.format(d) });
      }
      case "capacity":
      default: {
        const n = v as number;
        if (n === 0) return unlimited;
        return f.key === "runs_per_month"
          ? t("plans.perMonth", { n: fmt.format(n) })
          : fmt.format(n);
      }
    }
  };

  return (
    <section className="card dash-panel" style={{ marginTop: "var(--space-4)" }}>
      <div className="dash-panel-head">
        <h2>
          <CreditCard size={ICON.lg} style={{ marginRight: 8, verticalAlign: -3 }} />
          {t("plans.compareTitle")}
        </h2>
      </div>
      <div className="plans-grid">
        {plans.map((p, i) => (
          <PlanCard
            key={p.id}
            plan={p}
            current={current}
            // Diff each tier against the one below it in the ladder (Free has
            // none, Pro vs Free, Enterprise vs Pro) rather than the viewer's
            // current plan — so the "what this tier adds" cues show each step's
            // incremental value, not Enterprise's gap over Free.
            baseline={plans[i - 1]}
            info={info}
            redirecting={redirecting}
            t={t}
            formatValue={formatValue}
            onUpgrade={onUpgrade}
            onManage={onManage}
          />
        ))}
      </div>
    </section>
  );
}

function PlanCard({
  plan,
  current,
  baseline,
  info,
  redirecting,
  t,
  formatValue,
  onUpgrade,
  onManage,
}: {
  plan: PlanOption;
  current: PlanOption;
  // The tier directly below this one in the ladder; undefined for the lowest
  // card. The feature diff highlights what this tier adds over it.
  baseline?: PlanOption;
  info: PlansInfo;
  redirecting: boolean;
  t: (k: string, o?: Record<string, unknown>) => string;
  formatValue: (f: Feature, v: number | boolean) => ReactNode;
  onUpgrade: () => void;
  onManage: () => void;
}) {
  const isCurrent = plan.is_current;
  // An upgrade target: a higher (pro) plan the org isn't already on. Stripe
  // self-serve only handles Pro; custom comp tiers are admin-assigned, and the
  // sales-led Enterprise card (is_contact) gets its own "Contact sales" CTA.
  const isUpgradeTarget =
    !isCurrent && !plan.is_contact && plan.plan === "pro" && current.plan !== "pro";
  // The built-in Pro plan is the headline tier — flag it "Most popular" and
  // give it the featured treatment (unless it's already the current plan).
  const isPopular = plan.id === "pro" && !isCurrent;
  // Short marketing line per plan; falls back to nothing for custom comp tiers.
  const tagline = t(`plans.tagline.${plan.id}`, { defaultValue: "" });
  // Price line: only Free has a public figure in-app; paid tiers point to the
  // CTA / sales rather than committing a number here.
  const priceLine =
    plan.id === "free"
      ? t("plans.priceFree")
      : plan.is_contact
        ? t("plans.priceCustom")
        : t("plans.pricePaid");

  const cardClass =
    "card plan-card" +
    (isCurrent ? " plan-card-current" : "") +
    (isPopular ? " plan-card-featured" : "");

  const cta = plan.is_contact ? (
    <ButtonLink variant="secondary" block href={t("plans.contactSalesHref")}>
      {t("plans.contactSales")}
    </ButtonLink>
  ) : isCurrent ? (
    info.can_manage ? (
      <Button variant="secondary" block disabled={redirecting} onClick={onManage}>
        {t("plans.manageBilling")}
      </Button>
    ) : (
      <Button variant="secondary" block disabled>
        {t("plans.currentPlan")}
      </Button>
    )
  ) : isUpgradeTarget ? (
    info.can_upgrade ? (
      <Button variant="primary" block disabled={redirecting} onClick={onUpgrade}>
        <Sparkles size={ICON.sm} style={{ marginRight: 6 }} />
        {t("plans.upgradeTo", { plan: plan.name })}
      </Button>
    ) : (
      <div className="sub plan-contact">{t("plans.contactAdmin")}</div>
    )
  ) : null; // e.g. Free when you're on Pro — no action; the CTA row keeps its height

  return (
    <div className={cardClass}>
      <div className="plan-card-head">
        <h2>{plan.name}</h2>
        {isCurrent && <span className="plan-badge">{t("plans.yourPlan")}</span>}
      </div>
      {tagline && <p className="plan-tagline">{tagline}</p>}
      <div className="plan-price">{priceLine}</div>

      <div className="plan-card-cta">{cta}</div>

      <ul className="plan-feats">
        {FEATURES.map((f) => {
          const v = plan.limits[f.key];
          // Highlight a feature as an upgrade when this tier beats the one
          // below it (the baseline). The current plan and the lowest tier
          // (no baseline) show no upgrade cues.
          const up =
            !isCurrent && baseline != null && betterThan(f, v, baseline.limits[f.key]);
          return (
            <li key={f.key} className={"plan-feat" + (up ? " plan-feat-up" : "")}>
              <span className="plan-feat-ico">
                {f.kind === "bool" ? (
                  v ? (
                    <Check size={ICON.sm} />
                  ) : (
                    <Minus size={ICON.sm} />
                  )
                ) : up ? (
                  <ArrowUp size={ICON.sm} />
                ) : (
                  <Check size={ICON.sm} />
                )}
              </span>
              <span className="plan-feat-label">{t(f.labelKey)}</span>
              <span className="plan-feat-val">{formatValue(f, v)}</span>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
