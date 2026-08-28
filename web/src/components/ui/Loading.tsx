// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { CSSProperties } from "react";
import { useTranslation } from "react-i18next";
import { Notice } from "./Notice";

// Loading is "we are fetching this" — the one placeholder that stands where
// content will appear.
//
// `common.loading` is a single string, and it was being rendered ten different
// ways: a bare card, a muted card, `<p className="desc">`, `<p className="muted">`,
// `.files-empty`, `.history-empty`, `.dash-empty`, `.connection-note`, a bare
// `<div>`, and a bare `<p>`. Same word, ten sizes and colours, so moving between
// two pages of the app made the same wait look like two different things.
//
// The component takes the string as well as the shape: there is no reason for a
// call site to name the key, and a Loading that could say something else would
// drift straight back into ten variants.
export function Loading({
  inline,
  style,
  className,
}: {
  // Drops the card chrome for a centred line — inside a dialog, panel or list.
  inline?: boolean;
  style?: CSSProperties;
  className?: string;
}) {
  const { t } = useTranslation();
  return (
    // role="status" so a screen reader says "Loading…" when this appears. The
    // 51 hand-written versions said nothing at all, which left a non-sighted
    // user with a page that had simply gone quiet.
    <Notice inline={inline} style={style} className={className} role="status">
      {t("common.loading")}
    </Notice>
  );
}
