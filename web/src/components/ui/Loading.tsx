// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { CSSProperties } from "react";
import { useTranslation } from "react-i18next";
import { Notice } from "./Notice";

// Loading is "we are fetching this" — the one placeholder that stands where
// content will appear.
//
// `common.loading` is a single string, and it was being rendered ten different
// ways: bare cards, muted cards, four different empty-state classes, a bare
// <div>, a bare <p>. Same word, ten sizes and colours, so moving between two
// pages made the same wait look like two different things.
//
// The component takes the string as well as the shape: a Loading that could say
// something else would drift straight back into ten variants.
export function Loading({
  inline,
  style,
  className,
}: {
  inline?: boolean;
  style?: CSSProperties;
  className?: string;
}) {
  const { t } = useTranslation();
  return (
    <Notice inline={inline} style={style} className={className} role="status">
      {t("common.loading")}
    </Notice>
  );
}
