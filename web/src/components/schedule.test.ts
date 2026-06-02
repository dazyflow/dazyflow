import { describe, expect, it } from "vitest";
import { scheduleFromCron } from "./TriggersModal";

// scheduleFromCron is the parse half of the cron-preset round-trip.
// The picker emits cron via scheduleToCron and re-parses on every
// load; if these two ever drift, a user's "every day at 9" silently
// becomes "custom" on reload. The tests cover the round-trippable
// preset shapes and the fallthroughs to "custom".

describe("scheduleFromCron", () => {
  it("falls back to a sensible daily default on empty input", () => {
    expect(scheduleFromCron("")).toEqual({
      kind: "daily",
      hour: 9,
      minute: 0,
    });
  });

  it("recognises hourly at minute 0", () => {
    expect(scheduleFromCron("0 * * * *")).toEqual({
      kind: "hourly",
      minute: 0,
    });
  });

  it("recognises hourly at minute 30", () => {
    expect(scheduleFromCron("30 * * * *")).toEqual({
      kind: "hourly",
      minute: 30,
    });
  });

  it("recognises daily at 09:00", () => {
    expect(scheduleFromCron("0 9 * * *")).toEqual({
      kind: "daily",
      hour: 9,
      minute: 0,
    });
  });

  it("recognises daily at 17:30", () => {
    expect(scheduleFromCron("30 17 * * *")).toEqual({
      kind: "daily",
      hour: 17,
      minute: 30,
    });
  });

  it("recognises weekly on Monday at 09:00", () => {
    expect(scheduleFromCron("0 9 * * 1")).toEqual({
      kind: "weekly",
      days: [1],
      hour: 9,
      minute: 0,
    });
  });

  it("recognises weekly on Mon+Wed+Fri at 08:30", () => {
    expect(scheduleFromCron("30 8 * * 1,3,5")).toEqual({
      kind: "weekly",
      days: [1, 3, 5],
      hour: 8,
      minute: 30,
    });
  });

  it("normalises Sunday=7 to Sunday=0", () => {
    expect(scheduleFromCron("0 9 * * 7")).toEqual({
      kind: "weekly",
      days: [0],
      hour: 9,
      minute: 0,
    });
  });

  it("recognises monthly on the 1st at 09:00", () => {
    expect(scheduleFromCron("0 9 1 * *")).toEqual({
      kind: "monthly",
      day: 1,
      hour: 9,
      minute: 0,
    });
  });

  it("recognises monthly on the 15th at 12:00", () => {
    expect(scheduleFromCron("0 12 15 * *")).toEqual({
      kind: "monthly",
      day: 15,
      hour: 12,
      minute: 0,
    });
  });

  it("falls through to custom for ranges (1-5)", () => {
    expect(scheduleFromCron("0 9 * * 1-5")).toEqual({
      kind: "custom",
      cron: "0 9 * * 1-5",
    });
  });

  it("falls through to custom for step values (*/15)", () => {
    expect(scheduleFromCron("*/15 * * * *")).toEqual({
      kind: "custom",
      cron: "*/15 * * * *",
    });
  });

  it("falls through to custom when month is gated", () => {
    expect(scheduleFromCron("0 9 1 1 *")).toEqual({
      kind: "custom",
      cron: "0 9 1 1 *",
    });
  });

  it("falls through to custom for malformed input (too few fields)", () => {
    expect(scheduleFromCron("0 9")).toEqual({
      kind: "custom",
      cron: "0 9",
    });
  });

  it("falls through to custom for non-numeric minute", () => {
    expect(scheduleFromCron("xx * * * *")).toEqual({
      kind: "custom",
      cron: "xx * * * *",
    });
  });

  it("falls through to custom when DOW values are out of range", () => {
    expect(scheduleFromCron("0 9 * * 8")).toEqual({
      kind: "custom",
      cron: "0 9 * * 8",
    });
  });
});
