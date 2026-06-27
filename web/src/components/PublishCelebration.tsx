// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { Rocket } from "lucide-react";

// PublishCelebration is the one-shot "it went live!" flourish shown for ~1.6s
// after a successful publish — a rocket that launches up through an expanding
// burst ring with a "Published!" callout. Mount it conditionally (the parent
// clears the flag on a timer); it portals to <body>, sits above everything,
// and never intercepts clicks (pointer-events: none). Honors
// prefers-reduced-motion via CSS — the ring/launch collapse to a quick fade.
export function PublishCelebration() {
  const { t } = useTranslation();
  return createPortal(
    <div className="publish-celebration" aria-hidden="true">
      <div className="publish-celebration-stage">
        <span className="publish-celebration-ring" />
        <span className="publish-celebration-ring publish-celebration-ring-2" />
        <span className="publish-celebration-rocket">
          <Rocket size={44} strokeWidth={2.2} />
        </span>
        <span className="publish-celebration-text">{t("editor.publishedCheer")}</span>
      </div>
    </div>,
    document.body,
  );
}
