// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslation } from "react-i18next";
import { Table2 } from "lucide-react";
import { api } from "../../api";
import { useAuth } from "../../auth";
import { Callout } from "../ui/Callout";
import type { CollectionShareLink } from "../../types";
import { ICON } from "../../icons";
import { ShareLinkModal, useShareLink } from "./ShareLinkModal";

// ShareCollectionModal manages one collection's public link — the cryptic,
// login-free URL to a read-only table of its rows.
//
// The warning above the button is not decoration. The workspace-overview link
// publishes a sanitized snapshot, so its dialog can be breezy; this one
// publishes the collection's actual rows to anyone holding the URL, and the
// person clicking may not have thought about what is in there. So the
// consequence is stated before the link exists, not after.
export function ShareCollectionModal({
  collection,
  onClose,
  onChange,
}: {
  collection: string;
  onClose: () => void;
  onChange?: (link: CollectionShareLink | null) => void;
}) {
  const { t } = useTranslation();
  const { token, activeTenant, activeWorkspace } = useAuth();
  const tenant = activeTenant || undefined;
  const workspace = activeWorkspace || undefined;

  const share = useShareLink<CollectionShareLink>(
    {
      enabled: !!token,
      get: () => api.getCollectionShare(token!, collection, tenant, workspace),
      create: () =>
        api.createCollectionShare(token!, collection, tenant, workspace),
      remove: () =>
        api.deleteCollectionShare(token!, collection, tenant, workspace),
      path: "/board/",
      forbiddenMessage: t("shareCollection.forbidden"),
      onChange,
    },
    [token, collection, tenant, workspace],
  );

  return (
    <ShareLinkModal
      share={share}
      title={t("shareCollection.title", { name: collection })}
      description={t("shareCollection.description")}
      openIcon={<Table2 size={ICON.xs} />}
      labels={{
        open: t("shareCollection.open"),
        create: t("shareCollection.create"),
        regenerate: t("shareCollection.regenerate"),
        disable: t("shareCollection.disable"),
      }}
      linkNotice={
        <div style={{ marginTop: "var(--space-3)" }}>
          <Callout variant="warning">{t("shareCollection.liveWarning")}</Callout>
        </div>
      }
      noLinkNotice={
        <Callout variant="warning">{t("shareCollection.beforeYouShare")}</Callout>
      }
      onClose={onClose}
    />
  );
}
