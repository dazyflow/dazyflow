// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { CSSProperties } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { supportContactHref } from "../lib/supportContact";

// ContactSupportLink renders a real "Contact support" link built from the
// operator-configured support_contact (WhoAmI.support_contact). It renders
// NOTHING when no contact is configured, so callers can safely drop it next
// to a generic "something went wrong — contact support" message without
// risking a dead link. This is what makes that copy actionable anywhere in
// the app, not just on the Connections setup banner.
//
// An email contact opens the user's mail client; a URL contact opens in a
// new tab. For run-failure surfaces that can prefill diagnostics, build the
// href with supportContactWithContext instead and render your own link.
export function ContactSupportLink({
  className,
  style,
  label,
}: {
  className?: string;
  style?: CSSProperties;
  label?: string;
}) {
  const { t } = useTranslation();
  const { me } = useAuth();
  const href = supportContactHref(me?.support_contact);
  if (!href) return null;
  const external = !href.startsWith("mailto:");
  return (
    <a
      className={className}
      style={style}
      href={href}
      {...(external ? { target: "_blank", rel: "noreferrer noopener" } : {})}
    >
      {label ?? t("common.contactSupport")}
    </a>
  );
}
