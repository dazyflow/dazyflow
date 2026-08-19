// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Mirrors core/flowstatus_test.go. The two implementations must agree — the
// chip is the user's only readout of a rule the daemon enforces.
import { describe, expect, it } from "vitest";
import { flowRunStatus, flowRunStatusPublished } from "./flowStatus";

const cron = [{ module: "cron_trigger", params: { cron: "0 9 * * *" } }];
const webhook = [{ module: "webhook_input", params: { secrets: ["s"] } }];
const mention = [{ module: "slack_on_mention", params: {} }];
const bare = [{ module: "delay", params: { ms: 1 } }];

describe("flowRunStatusPublished", () => {
  it("treats an unpublished flow as not-published whatever the trigger", () => {
    // The regression this guards: webhooks used to fire while unpublished, so
    // this returned "live" for the webhook case and hid a real difference.
    expect(flowRunStatusPublished(false, [], cron, false)).toBe("needs_publish");
    expect(flowRunStatusPublished(false, [], webhook, false)).toBe("needs_publish");
    expect(flowRunStatusPublished(false, [], mention, false)).toBe("needs_publish");
  });

  it("is live once published", () => {
    expect(flowRunStatusPublished(false, [], cron, true)).toBe("live");
    expect(flowRunStatusPublished(false, [], webhook, true)).toBe("live");
    expect(flowRunStatusPublished(false, [], mention, true)).toBe("live");
  });

  it("keeps off/manual ahead of the publish rule", () => {
    expect(flowRunStatusPublished(true, [], cron, false)).toBe("paused");
    expect(flowRunStatusPublished(false, [], bare, false)).toBe("manual");
  });

  it("counts provider-event triggers as automatic", () => {
    // Previously "manual": a flow whose only trigger was "On mention" claimed
    // it ran only on Run, while firing on every mention.
    expect(flowRunStatus(false, [], mention)).toBe("live");
    expect(flowRunStatus(false, [], bare)).toBe("manual");
  });
});
