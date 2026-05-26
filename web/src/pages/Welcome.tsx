import { Link } from "react-router-dom";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";

// Welcome is the post-signup landing wizard — the "first-run"
// surface from the T0-3 TODO. Intentionally simple: three CTAs that
// point at the highest-leverage next actions, plus a confirmation
// of the tenant the user just got. The full step-by-step walkthrough
// (templates gallery, guided node-drop tutorial) becomes useful once
// templates ship; for now this is the right surface for "you're in,
// here's what you can do."
export function Welcome() {
  const { t } = useTranslation();
  const { me } = useAuth();
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
        <p>{t("welcome.intro")}</p>
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
