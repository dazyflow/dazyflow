// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslation } from "react-i18next";
import { Tv } from "lucide-react";
import { api } from "../../api";
import { useAuth } from "../../auth";
import type { ShareLink } from "../../types";
import { ICON } from "../../icons";
import { ShareLinkModal, useShareLink } from "./ShareLinkModal";

// ShareOverviewModal manages the workspace's single public overview link —
// the cryptic, login-free TV-dashboard URL. It loads the existing link on
// open, lets the user create one if none exists, copy it, open it, and
// regenerate (which invalidates the old link) or disable it.
export function ShareOverviewModal({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const { token, activeTenant, activeWorkspace } = useAuth();
  const tenant = activeTenant || undefined;
  const workspace = activeWorkspace || undefined;

  const share = useShareLink<ShareLink>(
    {
      enabled: !!token,
      get: () => api.getShare(token!, tenant, workspace),
      create: () => api.createShare(token!, tenant, workspace),
      remove: () => api.deleteShare(token!, tenant, workspace),
      path: "/tv/",
      forbiddenMessage: t("share.forbidden"),
    },
    [token, tenant, workspace],
  );

  return (
    <ShareLinkModal
      share={share}
      title={t("share.title")}
      description={t("share.description")}
      openIcon={<Tv size={ICON.xs} />}
      labels={{
        open: t("share.open"),
        create: t("share.create"),
        regenerate: t("share.regenerate"),
        disable: t("share.disable"),
      }}
      linkNotice={
        <p className="desc" style={{ marginTop: "var(--space-3)" }}>
          {t("share.warning")}
        </p>
      }
      noLinkNotice={<p className="desc">{t("share.none")}</p>}
      onClose={onClose}
    />
  );
}
