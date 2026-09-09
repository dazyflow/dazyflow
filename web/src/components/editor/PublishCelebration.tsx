// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";

// The one-shot confirmation shown when a publish lands.
//
// Publishing is the most consequential button in this product — it is what makes
// a flow start receiving — so the feedback should carry weight, not delight. The
// rocket-and-rings version that preceded this read as a consumer-app flourish
// and lasted long enough (1.6s) that it had to be waited out.
//
// So: a small card, one drawn check, a single quiet halo, and words that say
// what changed rather than congratulating anybody. Nothing bounces, overshoots
// or spins. The check is inline SVG because it is drawn rather than shown — a
// mark being made reads as "this has been recorded", where a mark that simply
// appears is just an icon.
//
// Mount it conditionally (the parent clears the flag on a timer). It portals to
// <body>, sits above everything, and never intercepts clicks.
// prefers-reduced-motion is honoured in CSS: no draw, no travel, a plain fade.
export function PublishCelebration() {
  const { t } = useTranslation();
  return createPortal(
    <div className="publish-live" aria-hidden="true">
      <div className="publish-live-card">
        <span className="publish-live-mark">
          <span className="publish-live-halo" />
          <svg viewBox="0 0 32 32" className="publish-live-check" aria-hidden="true">
            <circle
              className="publish-live-check-ring"
              cx="16"
              cy="16"
              r="14.5"
              fill="none"
              strokeWidth="1.5"
            />
            <path
              className="publish-live-check-tick"
              d="M10 16.6l4.2 4.2L22.4 12"
              fill="none"
              strokeWidth="2.4"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        </span>
        <span className="publish-live-words">
          <strong className="publish-live-title">{t("editor.publishedCheer")}</strong>
          <span className="publish-live-sub">{t("editor.publishedCheerSub")}</span>
        </span>
      </div>
    </div>,
    document.body,
  );
}
