// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ArrowRight, Plus, Sparkles } from "lucide-react";
import { FlowIcon } from "../icons";
import { api } from "../api";
import { useAuth } from "../auth";
import { loadRecentFlow, userScope } from "../recentFlow";

// Welcome is the post-signup landing wizard — the "first-run"
// surface. Intentionally simple: a confirmation of the tenant the user
// just got, a resume-where-you-left-off link, and a single primary CTA
// into the unified Create-flow page (blank / AI / template). All the
// "pick a goal" branching now lives inside that page's template tab,
// so Welcome stays a one-decision screen.

// HAS_FLOWS_KEY mirrors App.tsx's RootRedirect signal. We read it
// to decide between first-time and returning copy: a user who's
// already built a flow gets a quieter "Welcome back" headline
// instead of the full onboarding wizard tone.
const HAS_FLOWS_KEY = "dazyflow.hasFlows";

export function Welcome() {
  const { t } = useTranslation();
  const { me, token, activeTenant, activeWorkspace } = useAuth();
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
        <Link to="/flows/new?tab=blank" className="primary welcome-cta welcome-create">
          <Plus size={16} /> {t("welcome.createCta")}
        </Link>
        {me && (
          <div className="welcome-mcp">
            <div className="welcome-mcp-body">
              <div className="welcome-mcp-title">
                <Sparkles size={16} style={{ marginRight: 8, verticalAlign: -2 }} />
                {t("welcome.aiBuildTitle")}
              </div>
              <div className="welcome-mcp-desc">
                {t("welcome.aiBuildDesc")}
              </div>
            </div>
            {/* In-app AI describe-box: /flows/new is AI-first by default, so
                this lands straight on the "describe what you want" field with
                no setup. */}
            <Link to="/flows/new" className="welcome-cta">
              <Sparkles size={14} /> {t("welcome.aiBuildCta")}
            </Link>
          </div>
        )}
      </div>
    </div>
  );
}
