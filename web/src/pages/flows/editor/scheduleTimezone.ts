// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { Graph } from "../../../types";
import { browserTimeZone } from "../../../components/editor/TriggersModal";

// stampScheduleTimezones fills a missing/blank tz on Schedule (cron_trigger)
// nodes with the viewer's browser zone, returning the node list and whether
// anything changed. Templates and older flows ship only `cron`; without a zone
// both the schedule and its fired_at run in UTC. The editor stamps the zone on
// add/edit, but a forked or pre-existing flow never went through that — so we
// heal it once on load (and persist, since a Run executes the SAVED graph).
export function stampScheduleTimezones(
  nodes: Graph["nodes"],
): { nodes: Graph["nodes"]; changed: boolean } {
  let changed = false;
  const out = (nodes ?? []).map((n) => {
    const tz = (n.params as { tz?: unknown } | undefined)?.tz;
    if (n.module === "cron_trigger" && !(typeof tz === "string" && tz.trim())) {
      changed = true;
      return { ...n, params: { ...(n.params ?? {}), tz: browserTimeZone() } };
    }
    return n;
  });
  return { nodes: out, changed };
}
