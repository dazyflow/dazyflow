// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  describeSchedule,
  formatNextRun,
  summarizeFlowSchedule,
} from "./schedule";
import type { ScheduleEntry } from "../types";

// Stand-in for i18next's t, same shape as datetime.test.ts: resolves the
// schedules.* keys to their English templates and interpolates. formatNextRun
// passes the BASE key for the pluralised relInMinutes and lets i18next pick,
// so the plural it would choose is inlined here.
const en: Record<string, string> = {
  "schedules.everyHours": "every {{count}}h",
  "schedules.everyMinutes": "every {{count}}m",
  "schedules.everySeconds": "every {{count}}s",
  "schedules.relToday": "Today {{time}}",
  "schedules.relTomorrow": "Tomorrow {{time}}",
  "schedules.relSoon": "in less than a minute",
  "schedules.relInMinutes": "in {{count}} minutes",
};
const t = (k: string, o?: Record<string, unknown>): string => {
  let s = en[k] ?? k;
  if (o) for (const [key, v] of Object.entries(o)) s = s.replace(`{{${key}}}`, String(v));
  return s;
};

function entry(over: Partial<ScheduleEntry> = {}): ScheduleEntry {
  return {
    flow_id: "f1",
    graph_id: "g1",
    node_id: "n1",
    kind: "poll",
    disabled: false,
    flow_disabled: false,
    ...over,
  };
}

describe("describeSchedule", () => {
  it("shows a cron trigger's raw expression", () => {
    expect(describeSchedule(entry({ kind: "cron", cron: "0 9 * * 1" }), t)).toBe("0 9 * * 1");
  });

  // A cron node with no expression yet (just dropped on the canvas) must not
  // render "undefined".
  it("falls back to a dash for a cron with no expression", () => {
    expect(describeSchedule(entry({ kind: "cron" }), t)).toBe("—");
  });

  // The unit is picked by exact divisibility, largest first, so a poll reads
  // "every 2h" rather than "every 120m".
  it("picks the largest exact unit for a poll interval", () => {
    expect(describeSchedule(entry({ interval_seconds: 7200 }), t)).toBe("every 2h");
    expect(describeSchedule(entry({ interval_seconds: 3600 }), t)).toBe("every 1h");
    expect(describeSchedule(entry({ interval_seconds: 900 }), t)).toBe("every 15m");
    expect(describeSchedule(entry({ interval_seconds: 90 }), t)).toBe("every 90s");
    expect(describeSchedule(entry({ interval_seconds: 45 }), t)).toBe("every 45s");
  });

  // A missing interval is treated as 0, which is divisible by 3600 — so this
  // documents that a poll with no interval reads as "every 0h".
  it("treats a missing interval as zero", () => {
    expect(describeSchedule(entry({}), t)).toBe("every 0h");
  });
});

describe("formatNextRun", () => {
  // Fixed "now" so the relative branches are deterministic. Local time is
  // whatever the test env uses; every assertion below is built from the same
  // Date maths the function does, so it holds in any timezone.
  const now = new Date("2026-06-23T12:00:00Z");

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(now);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  const clock = (d: Date): string =>
    `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;

  it("counts down in minutes under an hour out", () => {
    const in20 = new Date(now.getTime() + 20 * 60000);
    expect(formatNextRun(in20.toISOString(), t)).toBe("in 20 minutes");
  });

  it("says 'soon' when the next fire is seconds away", () => {
    const in10s = new Date(now.getTime() + 10_000);
    expect(formatNextRun(in10s.toISOString(), t)).toBe("in less than a minute");
  });

  // The documented <60 guard: 59m40s rounds to 60 minutes, which must NOT
  // render "in 60 minutes" — it falls through to the Today branch.
  it("never renders 'in 60 minutes'", () => {
    const almostHour = new Date(now.getTime() + (59 * 60 + 40) * 1000);
    const got = formatNextRun(almostHour.toISOString(), t);
    expect(got).not.toContain("60 minutes");
    expect(got).toBe(`Today ${clock(almostHour)}`);
  });

  it("uses Today for a later fire on the same local day", () => {
    // Two hours out is past the minute-countdown window.
    const in2h = new Date(now.getTime() + 2 * 3600_000);
    const got = formatNextRun(in2h.toISOString(), t);
    // Only assert the Today branch when 2h really is still the same local day;
    // near midnight in some zones it is tomorrow, and that is correct too.
    const expected = in2h.getDate() === now.getDate()
      ? `Today ${clock(in2h)}`
      : `Tomorrow ${clock(in2h)}`;
    expect(got).toBe(expected);
  });

  it("uses Tomorrow for the next local day", () => {
    const tomorrow = new Date(now);
    tomorrow.setDate(now.getDate() + 1);
    tomorrow.setHours(9, 30, 0, 0);
    expect(formatNextRun(tomorrow.toISOString(), t)).toBe(`Tomorrow ${clock(tomorrow)}`);
  });

  it("falls back to the full timestamp further out", () => {
    const nextWeek = new Date(now.getTime() + 7 * 24 * 3600_000);
    const got = formatNextRun(nextWeek.toISOString(), t);
    // The absolute branch is "YYYY-MM-DD HH:MM" — no relative wording.
    expect(got).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}$/);
  });

  // An unparseable instant must not render "NaN"; it degrades to the shared
  // formatter, which echoes the raw string back.
  it("degrades gracefully on an invalid instant", () => {
    expect(formatNextRun("not-a-date", t)).toBe("not-a-date");
  });
});

describe("summarizeFlowSchedule", () => {
  it("reports nothing for a flow with no triggers", () => {
    const s = summarizeFlowSchedule([]);
    // An empty flow is not "paused" — there is simply nothing scheduled.
    expect(s.flowDisabled).toBe(false);
    expect(s.active).toBe(false);
    expect(s.nextRun).toBeUndefined();
    expect(s.activeNodeIds).toEqual([]);
    expect(s.pausedNodeIds).toEqual([]);
  });

  it("splits active and per-trigger-paused nodes", () => {
    const s = summarizeFlowSchedule([
      entry({ node_id: "a" }),
      entry({ node_id: "b", disabled: true }),
      entry({ node_id: "c" }),
    ]);
    expect(s.activeNodeIds).toEqual(["a", "c"]);
    expect(s.pausedNodeIds).toEqual(["b"]);
    expect(s.active).toBe(true);
    expect(s.flowDisabled).toBe(false);
  });

  // flowDisabled is a whole-flow pause, so it requires EVERY entry to carry
  // the flag — one un-paused trigger means the flow is still scheduled.
  it("only reports flowDisabled when every trigger carries it", () => {
    expect(
      summarizeFlowSchedule([
        entry({ node_id: "a", flow_disabled: true }),
        entry({ node_id: "b", flow_disabled: true }),
      ]).flowDisabled,
    ).toBe(true);
    expect(
      summarizeFlowSchedule([
        entry({ node_id: "a", flow_disabled: true }),
        entry({ node_id: "b" }),
      ]).flowDisabled,
    ).toBe(false);
  });

  // A flow-level pause can't be lifted per-trigger, so those nodes appear in
  // neither list — offering "resume" on one would silently do nothing.
  it("omits flow-paused nodes from both toggle lists", () => {
    const s = summarizeFlowSchedule([
      entry({ node_id: "a", flow_disabled: true }),
      entry({ node_id: "b", flow_disabled: true, disabled: true }),
    ]);
    expect(s.activeNodeIds).toEqual([]);
    expect(s.pausedNodeIds).toEqual([]);
    expect(s.active).toBe(false);
  });

  it("takes the soonest upcoming fire across active triggers", () => {
    const s = summarizeFlowSchedule([
      entry({ node_id: "a", next_fires: ["2026-06-24T09:00:00Z", "2026-06-25T09:00:00Z"] }),
      entry({ node_id: "b", next_fires: ["2026-06-23T18:00:00Z"] }),
    ]);
    expect(s.nextRun).toBe("2026-06-23T18:00:00Z");
  });

  // A paused trigger's next_fires must not win the rollup — the flow list
  // would advertise a run that will never happen.
  it("ignores the next fire of a paused trigger", () => {
    const s = summarizeFlowSchedule([
      entry({ node_id: "a", next_fires: ["2026-06-24T09:00:00Z"] }),
      entry({ node_id: "b", disabled: true, next_fires: ["2026-06-23T01:00:00Z"] }),
      entry({ node_id: "c", flow_disabled: true, next_fires: ["2026-06-23T02:00:00Z"] }),
    ]);
    expect(s.nextRun).toBe("2026-06-24T09:00:00Z");
  });

  it("leaves nextRun unset when no active trigger has one", () => {
    const s = summarizeFlowSchedule([
      entry({ node_id: "a" }),
      entry({ node_id: "b", next_fires: [] }),
    ]);
    expect(s.nextRun).toBeUndefined();
    expect(s.active).toBe(true);
  });

  it("passes the original entries through untouched", () => {
    const entries = [entry({ node_id: "a" })];
    expect(summarizeFlowSchedule(entries).entries).toBe(entries);
  });
});
