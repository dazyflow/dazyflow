// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The signed-in catch-all. It used to be `<Navigate to="/flows" replace />`:
// a bookmark to a deleted flow, a link from an old email, or a typo silently
// teleported the user to the flow list. Nothing said a page had been asked for
// and not found, so the reader was left to work out whether they had
// mis-clicked, whether the thing they wanted was gone, or whether the product
// had just decided to go somewhere else.
//
// Saying so costs one screen and answers the question. The path is echoed back
// because half the time it is the evidence the reader needs — a truncated
// copy-paste, or an id they can now recognise as one they deleted.
import { Link, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { FileQuestion } from "lucide-react";

export function NotFound() {
  const { t } = useTranslation();
  const { pathname } = useLocation();

  return (
    <div className="notfound">
      <div className="card notfound-card">
        {/* Decorative hero glyph, sized to the card rather than to the icon
            scale (see check-icon-sizes: >=22 is deliberately a literal). */}
        <FileQuestion size={36} className="notfound-icon" />
        <h1>{t("notFound.title")}</h1>
        <p className="sub">{t("notFound.body")}</p>
        <code className="notfound-path">{pathname}</code>
        <p>
          <Link to="/flows">{t("notFound.backToFlows")}</Link>
        </p>
      </div>
    </div>
  );
}
