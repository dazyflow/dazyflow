// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ArrowRight, Sparkles } from "lucide-react";
import { FlowIcon, ICON } from "../icons";
import { api } from "../api";
import { useAuth } from "../auth";
import { loadRecentFlow, userScope } from "../recentFlow";

// Welcome is the post-signup landing wizard — the "first-run" surface.
// Intentionally simple: a resume-where-you-left-off link and one primary
// action, with the other two routes offered as quieter cards below.
//
// The primary action opens the no-setup starter flow and lands the user in
// the editor with something that actually runs. It used to be "Create your
// first flow" pointing at ?tab=blank — the EMPTY CANVAS, which is the hardest
// of the three ways in and the one most likely to make a non-technical user
// close the tab. The starter template needs no account, no connection and no
// trigger: press Run and output appears, which is the fastest honest answer
// to "what does this thing do?".

// HAS_FLOWS_KEY mirrors App.tsx's RootRedirect signal. We read it
// to decide between first-time and returning copy: a user who's
// already built a flow gets a quieter "Welcome back" headline
// instead of the full onboarding wizard tone.
const HAS_FLOWS_KEY = "dazyflow.hasFlows";

export function Welcome() {
  const { t } = useTranslation();
  const { me, token, activeTenant, activeWorkspace } = useAuth();
  // Scoped to the signed-in account AND the active org — an unscoped read
  // offered another user's flow on a shared browser, and keying on the home
  // tenant surfaced the previous org's flow after a switch. Recomputed when
  // `me` / activeTenant resolve.
  const recent = loadRecentFlow(userScope(activeTenant || me?.tenant, me?.subject));
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
  // Returning users see a quieter heading ("Welcome back to Dazy
  // Flow", "Pick up where you left off") instead of the first-run
  // wizard tone. Hint comes from the same localStorage flag
  // RootRedirect uses, so the two surfaces stay consistent.
  let isReturning = false;
  try {
    // Per-account key: a brand-new account on a browser where someone
    // else had flows must read "Welcome", not "Welcome back".
    isReturning =
      localStorage.getItem(
        `${HAS_FLOWS_KEY}.${userScope(activeTenant || me?.tenant, me?.subject)}`,
      ) === "1";
  } catch {
    /* private mode / strict iframe — treat as first-time */
  }
  return (
    <div className="welcome">
      <div className="card welcome-card">
        <h1>{isReturning ? t("welcome.titleReturning") : t("welcome.title")}</h1>
        {recent && (
          <Link
            to={`/flows/${encodeURIComponent(recent.id)}`}
            className="welcome-resume"
          >
            <span className="welcome-resume-icon">
              <FlowIcon icon={live?.icon ?? recent.icon} size={ICON.lg} />
            </span>
            <span className="welcome-resume-body">
              <span className="welcome-resume-lede">
                {t("welcome.continueTitle")}
              </span>
              <span className="welcome-resume-name">{live?.name ?? recent.name}</span>
            </span>
            <ArrowRight size={ICON.md} className="welcome-resume-arrow" />
          </Link>
        )}
        <p className="welcome-intro">
          {isReturning ? t("welcome.introReturning") : t("welcome.intro")}
        </p>
        {/* The no-setup starter, copied and opened in one click (?start= is
            handled by TemplateGallery). */}
        <Link
          to="/flows/new?tab=template&start=try-it-now"
          className="primary welcome-cta welcome-create"
        >
          <Sparkles size={ICON.md} /> {t("welcome.featuredCta")}
        </Link>
        <p className="welcome-featured-desc">{t("welcome.featuredDesc")}</p>
        <p className="welcome-alt">
          <Link to="/flows/new?tab=template">{t("welcome.browseTemplates")}</Link>
          {" · "}
          <Link to="/flows/new?tab=blank">{t("welcome.createCta")}</Link>
        </p>
        {me && (
          <div className="welcome-mcp">
            <div className="welcome-mcp-body">
              <div className="welcome-mcp-title">
                <Sparkles className="icon-lede" size={ICON.md} />
                {t("welcome.aiBuildTitle")}
              </div>
              <div className="welcome-mcp-desc">
                {t("welcome.aiBuildDesc")}
              </div>
            </div>
            {/* Explicit ?tab=ai — the create page defaults to templates now,
                so this has to name the tab it wants. */}
            <Link to="/flows/new?tab=ai" className="welcome-cta">
              <Sparkles size={ICON.sm} /> {t("welcome.aiBuildCta")}
            </Link>
          </div>
        )}
      </div>
    </div>
  );
}
