import { Link } from "react-router-dom";
import { Trans, useTranslation } from "react-i18next";
import { ArrowRight, Workflow } from "lucide-react";
import { useAuth } from "../auth";
import { loadRecentFlow } from "../recentFlow";

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
// Templates page filters on.
const GOALS: { category: string; titleKey: string; descKey: string }[] = [
  { category: "Get notified", titleKey: "welcome.goalNotifyTitle", descKey: "welcome.goalNotifyDesc" },
  { category: "Scheduled reports", titleKey: "welcome.goalReportTitle", descKey: "welcome.goalReportDesc" },
  { category: "Spreadsheets & data", titleKey: "welcome.goalDataTitle", descKey: "welcome.goalDataDesc" },
  { category: "For developer teams", titleKey: "welcome.goalDevTitle", descKey: "welcome.goalDevDesc" },
];

export function Welcome() {
  const { t } = useTranslation();
  const { me } = useAuth();
  // Resolved once on mount — localStorage only changes when the editor
  // mounts, which can't happen while this page is showing.
  const recent = loadRecentFlow();
  return (
    <div className="welcome">
      <div className="card welcome-card">
        <h1>{t("welcome.title")}</h1>
        {me?.subject && (
          <p className="welcome-sub">
            <Trans
              i18nKey="welcome.signedInAs"
              values={{ subject: me.subject }}
              components={[<strong />]}
            />
            {me.tenant && (
              <Trans
                i18nKey="welcome.inTenant"
                values={{ tenant: me.tenant }}
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
              <Workflow size={18} />
            </span>
            <span className="welcome-resume-body">
              <span className="welcome-resume-lede">
                {t("welcome.continueTitle")}
              </span>
              <span className="welcome-resume-name">{recent.name}</span>
            </span>
            <ArrowRight size={16} className="welcome-resume-arrow" />
          </Link>
        )}
        <p>{t("welcome.intro")}</p>
        <div className="welcome-goals">
          <div className="welcome-goals-head">{t("welcome.goalsTitle")}</div>
          <div className="welcome-goal-grid">
            {GOALS.map((g) => (
              <Link
                key={g.category}
                to={`/templates?category=${encodeURIComponent(g.category)}`}
                className="welcome-goal"
              >
                <span className="welcome-goal-title">{t(g.titleKey)}</span>
                <span className="welcome-goal-desc">{t(g.descKey)}</span>
              </Link>
            ))}
          </div>
        </div>
        <p className="welcome-or">{t("welcome.orExplore")}</p>
        <ol className="welcome-steps">
          <li>
            <h2>{t("welcome.step1Title")}</h2>
            <p>{t("welcome.step1Body")}</p>
            <Link to="/templates" className="primary welcome-cta">
              {t("welcome.step1Cta")}
            </Link>
          </li>
          <li>
            <h2>{t("welcome.step2Title")}</h2>
            <p>{t("welcome.step2Body")}</p>
            <Link to="/flows" className="welcome-cta">
              {t("welcome.step2Cta")}
            </Link>
          </li>
          <li>
            <h2>{t("welcome.step3Title")}</h2>
            <p>{t("welcome.step3Body")}</p>
            <Link to="/runs" className="welcome-cta">
              {t("welcome.step3Cta")}
            </Link>
          </li>
        </ol>
        <p className="welcome-foot">
          <Trans
            i18nKey="welcome.foot"
            components={[<Link to="/welcome" />]}
          />
        </p>
      </div>
    </div>
  );
}
