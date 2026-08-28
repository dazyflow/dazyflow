// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ArrowRight, FilePlus2, LayoutTemplate, Play, Sparkles } from "lucide-react";
import { FlowIcon, ICON } from "../icons";
import { api } from "../api";
import { useAuth } from "../auth";
import { loadRecentFlow, userScope } from "../recentFlow";

// Welcome is the post-signup landing — the "first-run" surface.
//
// It reads top to bottom as: who you are, where you were, what you can do
// next. That ordering is the whole rework. Before it, the page opened with a
// greeting, offered the resume link, and only THEN introduced itself — so the
// one line explaining the page arrived after the reader had already been given
// something to click. The featured button had the same problem in miniature:
// the sentence saying what pressing it would do sat underneath it.
//
// The four ways into a new flow were the deeper mess. All four go to the same
// place — /flows/new, differing only in ?tab= — but they were presented at
// three different weights: one accent button, two 13px text links separated by
// a dot, and a bordered card at the bottom of the page. Nothing about that
// ranking followed from how useful they are; describing a flow in English, the
// most distinctive of the three, was the one at the bottom in a box. They are
// now one row of equal cards, carrying the SAME icons as the tabs they land on
// (CreateFlow's tab strip), so the thing you pressed is the thing you arrive
// at.
//
// What survives untouched is the resume card: it is the only element here that
// knows anything about this particular user, which is exactly why it outranks
// every generic call to action on the page.

// HAS_FLOWS_KEY mirrors App.tsx's RootRedirect signal. We read it to decide
// between first-run and returning copy: someone who has already built a flow
// gets a quieter headline instead of the onboarding tone.
const HAS_FLOWS_KEY = "dazyflow.hasFlows";

// The three ways to start something new, in the order CreateFlow's tabs use.
// Icons match that tab strip on purpose — LayoutTemplate / Sparkles /
// FilePlus2 — so the card and the page it opens are recognisably the same
// route rather than two designs of the same idea.
const OPTIONS = [
  { key: "template", to: "/flows/new?tab=template", Icon: LayoutTemplate },
  { key: "ai", to: "/flows/new?tab=ai", Icon: Sparkles },
  { key: "blank", to: "/flows/new?tab=blank", Icon: FilePlus2 },
] as const;

export function Welcome() {
  const { t } = useTranslation();
  const { me, token, activeTenant, activeWorkspace } = useAuth();
  // Scoped to the signed-in account AND the active org — an unscoped read
  // offered another user's flow on a shared browser, and keying on the home
  // tenant surfaced the previous org's flow after a switch. Recomputed when
  // `me` / activeTenant resolve.
  const recent = loadRecentFlow(userScope(activeTenant || me?.tenant, me?.subject));
  // The cached recent flow only carries whatever icon/name was stored when it
  // was last opened. Resolve the current icon + name from the live flow list so
  // a renamed flow / freshly-set icon shows correctly here without depending on
  // the cache being fresh.
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

  // Returning users get a quieter heading. The hint comes from the same
  // localStorage flag RootRedirect uses, so the two surfaces agree.
  let isReturning = false;
  try {
    // Per-account key: a brand-new account on a browser where someone else had
    // flows must read "Welcome", not "Welcome back".
    isReturning =
      localStorage.getItem(
        `${HAS_FLOWS_KEY}.${userScope(activeTenant || me?.tenant, me?.subject)}`,
      ) === "1";
  } catch {
    /* private mode / strict iframe — treat as first-time */
  }

  // The zero-setup starter is a demonstration, and you only need it once.
  // Showing it to someone with a flow of their own to resume puts a toy above
  // their actual work. `recent` is checked as well as the flag because a
  // resumable flow is proof of use even where the flag never got written.
  const showDemo = !isReturning && !recent;

  return (
    <div className="welcome">
      <header className="welcome-head">
        <h1>{isReturning ? t("welcome.titleReturning") : t("welcome.title")}</h1>
        <p className="welcome-intro">
          {isReturning ? t("welcome.introReturning") : t("welcome.intro")}
        </p>
      </header>

      {recent && (
        <Link
          to={`/flows/${encodeURIComponent(recent.id)}`}
          className="welcome-resume"
        >
          <span className="welcome-resume-icon">
            <FlowIcon icon={live?.icon ?? recent.icon} size={ICON.lg} />
          </span>
          <span className="welcome-resume-body">
            <span className="welcome-resume-lede">{t("welcome.continueTitle")}</span>
            <span className="welcome-resume-name">{live?.name ?? recent.name}</span>
          </span>
          <ArrowRight size={ICON.md} className="welcome-resume-arrow" />
        </Link>
      )}

      {showDemo && (
        // One click to a flow that runs with no account, no connection and no
        // trigger. It is featured for a first-timer because the alternative
        // first move — an empty canvas — is the hardest of the three ways in
        // and the one most likely to end with the tab being closed.
        <Link
          to="/flows/new?tab=template&start=try-it-now"
          className="welcome-demo"
        >
          <span className="welcome-demo-icon">
            <Play size={ICON.lg} />
          </span>
          <span className="welcome-demo-body">
            <span className="welcome-demo-title">{t("welcome.demoTitle")}</span>
            {/* Inside the card and above the arrow: this is the sentence that
                tells you what the click does, so it has to arrive before the
                click, not under it. */}
            <span className="welcome-demo-desc">{t("welcome.demoDesc")}</span>
          </span>
          <ArrowRight size={ICON.md} className="welcome-demo-arrow" />
        </Link>
      )}

      <section>
        <h2 className="welcome-section-title">{t("welcome.startHeading")}</h2>
        <div className="welcome-options">
          {OPTIONS.map(({ key, to, Icon }) => (
            <Link key={key} to={to} className="welcome-option">
              <Icon size={ICON.lg} className="welcome-option-icon" />
              <span className="welcome-option-title">
                {t(`welcome.option.${key}.title`)}
              </span>
              <span className="welcome-option-desc">
                {t(`welcome.option.${key}.desc`)}
              </span>
            </Link>
          ))}
        </div>
      </section>
    </div>
  );
}
