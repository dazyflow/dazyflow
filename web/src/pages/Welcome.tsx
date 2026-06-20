import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Trans, useTranslation } from "react-i18next";
import { ArrowRight, Plug, Bell, Clock, Database, Zap } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { FlowIcon } from "../icons";
import { api } from "../api";
import { useAuth } from "../auth";
import { loadRecentFlow, userScope } from "../recentFlow";
import { shouldShowTenantID } from "../lib/visibleTenant";
import { orgDisplayName } from "../lib/orgDisplayName";
import { ConnectMcpClientModal } from "../components/ConnectMcpClientModal";

// Welcome is the post-signup landing wizard — the "first-run"
// surface from the T0-3 TODO. Intentionally simple: three CTAs that
// point at the highest-leverage next actions, plus a confirmation
// of the tenant the user just got. The full step-by-step walkthrough
// (templates gallery, guided node-drop tutorial) becomes useful once
// templates ship; for now this is the right surface for "you're in,
// here's what you can do."
// GOALS is the goal-first entry into the gallery: each card names an
// outcome in the user's words and routes to the matching template
// category. `category` MUST match the category strings in
// public/templates/index.json verbatim — that's the join key the
// Templates page filters on. The developer-focused category isn't
// in this grid — it sits as a small "More for developers →" link
// below the goals so a non-technical owner's eye lands on the
// three relevant cards instead of skipping past four.
const GOALS: {
  category: string;
  titleKey: string;
  descKey: string;
  Icon: LucideIcon;
}[] = [
  { category: "Get notified", titleKey: "welcome.goalNotifyTitle", descKey: "welcome.goalNotifyDesc", Icon: Bell },
  { category: "Scheduled reports", titleKey: "welcome.goalReportTitle", descKey: "welcome.goalReportDesc", Icon: Clock },
  { category: "Spreadsheets & data", titleKey: "welcome.goalDataTitle", descKey: "welcome.goalDataDesc", Icon: Database },
];

// DEV_CATEGORY is the developer-templates filter, surfaced as a
// secondary link under the goal grid. Same join key as the GOALS
// entries above so /templates?category= round-trips correctly.
const DEV_CATEGORY = "For developer teams";

// QUICKSTART_TEMPLATE is the one zero-setup flow we put front-and-centre
// above the goal grid. It must produce a visible result on a single Run
// click with nothing connected — `instant-summary` feeds a built-in
// sample dataset into a render step and shows the output right in the
// editor, so a brand-new owner sees a flow *work* before meeting any
// OAuth/secret wall the goal cards mostly route into. This deep-links
// straight to that single template via /templates?template=<id> (the
// focus param the Templates page filters on) so it lands on the card,
// not buried in a category list. Must match the template `id` in
// public/templates/index.json.
const QUICKSTART_TEMPLATE = "instant-summary";

// HAS_FLOWS_KEY mirrors App.tsx's RootRedirect signal. We read it
// to decide between first-time and returning copy: a user who's
// already built a flow gets a quieter "Welcome back" headline
// instead of the full onboarding wizard tone.
const HAS_FLOWS_KEY = "dazyflow.hasFlows";

export function Welcome() {
  const { t } = useTranslation();
  const { me, tenants, token, activeTenant, activeWorkspace } = useAuth();
  const [connectingMcp, setConnectingMcp] = useState(false);
  // Scoped to the signed-in account — an unscoped read offered another
  // user's flow on a shared browser. Recomputed when `me` resolves.
  const recent = loadRecentFlow(userScope(me));
  // The cached recent flow only carries whatever icon/name was stored
  // when it was last opened. Resolve the current icon + name from the
  // live flow list so a renamed flow / freshly-set icon shows correctly
  // here without depending on the cache being fresh.
  const recentId = recent?.id;
  const [live, setLive] = useState<{ icon?: string; name?: string } | null>(null);
  useEffect(() => {
    if (!recentId || !token || !activeWorkspace) return;
    let cancelled = false;
    api
      .listGraphs(token, activeTenant, activeWorkspace)
      .then((r) => {
        if (cancelled) return;
        const f = r.graphs.find((g) => g.id === recentId);
        if (f) setLive({ icon: f.icon, name: f.name });
      })
      .catch(() => {
        /* non-essential — fall back to the cached values */
      });
    return () => {
      cancelled = true;
    };
  }, [recentId, token, activeTenant, activeWorkspace]);
  // showTenant gates the "in tenant `usr_…`" suffix. For ordinary
  // single-tenant principals the identifier is internal noise; only
  // platform admins / multi-tenant principals see it as actionable
  // context. The trailing period sits outside the gate so the
  // sentence flows either way.
  const showTenant = shouldShowTenantID(me, tenants.length);
  // Returning users see a quieter heading ("Welcome back to Dazy
  // Flow", "Pick up where you left off") instead of the first-run
  // wizard tone. Hint comes from the same localStorage flag
  // RootRedirect uses, so the two surfaces stay consistent.
  let isReturning = false;
  try {
    // Per-account key: a brand-new account on a browser where someone
    // else had flows must read "Welcome", not "Welcome back".
    isReturning =
      localStorage.getItem(`${HAS_FLOWS_KEY}.${userScope(me)}`) === "1";
  } catch {
    /* private mode / strict iframe — treat as first-time */
  }
  return (
    <div className="welcome">
      <div className="card welcome-card">
        <h1>{isReturning ? t("welcome.titleReturning") : t("welcome.title")}</h1>
        {me?.subject && (
          <p className="welcome-sub">
            <Trans
              i18nKey="welcome.signedInAs"
              values={{ subject: me.subject }}
              components={[<strong />]}
            />
            {me.tenant && showTenant && (
              <Trans
                i18nKey="welcome.inTenant"
                values={{ tenant: orgDisplayName(me, me.tenant) }}
                components={[<code />]}
              />
            )}
            .
          </p>
        )}
        {recent && (
          <Link
            to={`/flows/${encodeURIComponent(recent.id)}`}
            className="welcome-resume"
          >
            <span className="welcome-resume-icon">
              <FlowIcon icon={live?.icon ?? recent.icon} size={18} />
            </span>
            <span className="welcome-resume-body">
              <span className="welcome-resume-lede">
                {t("welcome.continueTitle")}
              </span>
              <span className="welcome-resume-name">{live?.name ?? recent.name}</span>
            </span>
            <ArrowRight size={16} className="welcome-resume-arrow" />
          </Link>
        )}
        <p className="welcome-intro">
          {isReturning ? t("welcome.introReturning") : t("welcome.intro")}
        </p>
        <Link
          to={`/templates?template=${encodeURIComponent(QUICKSTART_TEMPLATE)}`}
          className="welcome-featured"
        >
          <span className="welcome-featured-icon">
            <Zap size={18} strokeWidth={2.2} />
          </span>
          <span className="welcome-featured-body">
            <span className="welcome-featured-title">
              {t("welcome.featuredTitle")}
            </span>
            <span className="welcome-featured-desc">
              {t("welcome.featuredDesc")}
            </span>
          </span>
          <span className="welcome-featured-cta">
            {t("welcome.featuredCta")}
            <ArrowRight size={15} />
          </span>
        </Link>
        <div className="welcome-goal-grid">
          {GOALS.map((g) => (
            <Link
              key={g.category}
              to={`/templates?category=${encodeURIComponent(g.category)}`}
              className="welcome-goal"
            >
              <div className="welcome-goal-head">
                <span className="welcome-goal-icon">
                  <g.Icon size={18} strokeWidth={2} />
                </span>
                <span className="welcome-goal-title">{t(g.titleKey)}</span>
              </div>
              <span className="welcome-goal-desc">{t(g.descKey)}</span>
            </Link>
          ))}
        </div>
        <div className="welcome-goal-dev">
          <Link to={`/templates?category=${encodeURIComponent(DEV_CATEGORY)}`}>
            {t("welcome.goalDevLink")}
          </Link>
        </div>
        {me && (
          <div className="welcome-mcp">
            <div className="welcome-mcp-body">
              <div className="welcome-mcp-title">
                <Plug size={16} style={{ marginRight: 8, verticalAlign: -2 }} />
                {t("welcome.connectMcpTitle")}
              </div>
              <div className="welcome-mcp-desc">
                {t("welcome.connectMcpDesc")}
              </div>
            </div>
            <button className="primary welcome-cta" onClick={() => setConnectingMcp(true)}>
              <Plug size={14} /> {t("welcome.connectMcpCta")}
            </button>
          </div>
        )}
      </div>
      {connectingMcp && (
        <ConnectMcpClientModal onClose={() => setConnectingMcp(false)} />
      )}
    </div>
  );
}
