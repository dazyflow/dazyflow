// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslation } from "react-i18next";
import { Tv } from "lucide-react";
import { api } from "../../api";
import type { ShareLink } from "../../types";
import { ICON } from "../../icons";
import { ShareLinkModal } from "./ShareLinkModal";

// ShareOverviewModal manages the workspace's single public overview link —
// the cryptic, login-free TV-dashboard URL. See ShareLinkModal for the
// load / create / copy / regenerate / disable behaviour.
export function ShareOverviewModal({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <ShareLinkModal<ShareLink>
      ops={{
        get: api.getShare,
        create: api.createShare,
        remove: api.deleteShare,
      }}
      scopeKey="overview"
      basePath="/tv"
      forbiddenMessage={t("share.forbidden")}
      title={t("share.title")}
      description={t("share.description")}
      openLabel={t("share.open")}
      openIcon={<Tv size={ICON.xs} />}
      createLabel={t("share.create")}
      regenerateLabel={t("share.regenerate")}
      disableLabel={t("share.disable")}
      linkNotice={<p className="desc">{t("share.warning")}</p>}
      emptyNotice={<p className="desc">{t("share.none")}</p>}
      onClose={onClose}
    />
  );
}
