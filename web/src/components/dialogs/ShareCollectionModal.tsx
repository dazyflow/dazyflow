// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslation } from "react-i18next";
import { Table2 } from "lucide-react";
import { api } from "../../api";
import { Callout } from "../ui/Callout";
import type { CollectionShareLink } from "../../types";
import { ICON } from "../../icons";
import { ShareLinkModal } from "./ShareLinkModal";

// ShareCollectionModal manages one collection's public link — the cryptic,
// login-free URL to a read-only table of its rows. See ShareLinkModal for the
// load / create / copy / regenerate / disable behaviour.
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
  return (
    <ShareLinkModal<CollectionShareLink>
      ops={{
        get: (token, tenant, ws) =>
          api.getCollectionShare(token, collection, tenant, ws),
        create: (token, tenant, ws) =>
          api.createCollectionShare(token, collection, tenant, ws),
        remove: (token, tenant, ws) =>
          api.deleteCollectionShare(token, collection, tenant, ws),
      }}
      scopeKey={collection}
      basePath="/board"
      forbiddenMessage={t("shareCollection.forbidden")}
      onChange={onChange}
      title={t("shareCollection.title", { name: collection })}
      description={t("shareCollection.description")}
      openLabel={t("shareCollection.open")}
      openIcon={<Table2 size={ICON.xs} />}
      createLabel={t("shareCollection.create")}
      regenerateLabel={t("shareCollection.regenerate")}
      disableLabel={t("shareCollection.disable")}
      linkNotice={
        <Callout variant="warning">{t("shareCollection.liveWarning")}</Callout>
      }
      emptyNotice={
        <Callout variant="warning">
          {t("shareCollection.beforeYouShare")}
        </Callout>
      }
      onClose={onClose}
    />
  );
}
