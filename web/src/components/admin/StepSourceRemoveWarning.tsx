// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useTranslation } from "react-i18next";
import type { StepSourceUsage } from "../../types";

// The sentence next to the Remove button on a step-source admin page.
//
// Four states, because there are four genuinely different situations and both
// pages used to render one sentence for all of them ("flows using its steps
// will stop running") whether or not any flow did. That is a warning nobody
// can act on, and it made cleaning up a source added by mistake feel dangerous.
//
// Shared by MCP servers and web APIs rather than copied: they ask the same
// question of the same scan, and the interesting states here are the easy ones
// to get wrong — a failed lookup must not read as "safe to delete", and a
// count of flows the admin cannot see must not render as an empty list.
//
// `ns` is the i18n namespace of the page using it ("mcp", "webapi"), which
// owns the wording; the shape of the decision lives here.
export function StepSourceRemoveWarning({
  usage,
  ns,
}: {
  usage?: StepSourceUsage;
  ns: "mcp" | "webapi";
}) {
  const { t } = useTranslation();
  // No answer yet, or the lookup failed: warn unconditionally. Never the
  // "nothing uses it" line, which would be a claim we cannot make.
  if (!usage) return <>{t(`${ns}.removeReally`)}</>;

  const total = usage.flows.length + usage.hidden;
  if (total === 0) return <>{t(`${ns}.removeUnused`)}</>;

  // Published ones are already sorted to the front by the daemon. The list is
  // capped: a warning is for deciding, not for auditing.
  const shown = usage.flows.slice(0, 3);
  const names = shown.map((f) => f.name || f.flow_id).join(", ");
  const rest = total - shown.length;
  const published = usage.flows.some((f) => f.published);

  // Every user is a flow this admin may not view: the count is the whole
  // warning, and a sentence ending in an empty list would be worse than one
  // that says so.
  if (names === "") {
    return (
      <>
        {t(`${ns}.removeInUseHidden`, { count: total })}
        {published && ` ${t(`${ns}.removePublished`)}`}
      </>
    );
  }
  return (
    <>
      {t(`${ns}.removeInUse`, { count: total, flows: names })}
      {rest > 0 && ` ${t(`${ns}.andMore`, { n: rest })}`}
      {published && ` ${t(`${ns}.removePublished`)}`}
    </>
  );
}
