// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The samples are fed to the same parsers a real delivery goes through, so the
// daemon refuses any that would not have reached the trigger: a Slack event of
// another type, a pull request that is not newly opened. A sample that trips
// one of those rules would 400 on the first click, so the rules are asserted
// here rather than discovered by hand.

import { describe, expect, it } from "vitest";
import { TEST_TRIGGER_MODULES, buildTriggerSample, canTestFire } from "./testTrigger";

describe("test-fire samples", () => {
  it("offers one for every event trigger the daemon can seed", () => {
    // The webhook family is deliberately absent: its payload comes from the
    // step's own declared form fields, not from a provider's event shape.
    for (const module of ["slack_on_mention", "github_on_push", "github_on_new_pr"]) {
      expect(buildTriggerSample(module), module).not.toBeNull();
    }
    for (const module of ["webhook_input", "request_input", "form_input"]) {
      expect(buildTriggerSample(module), module).toBeNull();
    }
  });

  it("sends Slack the Events API envelope, so the Workspace port is filled", () => {
    const s = buildTriggerSample("slack_on_mention") as Record<string, unknown>;
    expect(s.team_id).toBeTruthy();
    const ev = s.event as Record<string, unknown>;
    // app_mention or the daemon refuses it — this trigger fires on nothing else.
    expect(ev.type).toBe("app_mention");
    for (const port of ["user", "text", "channel", "ts"]) {
      expect(ev[port], port).toBeTruthy();
    }
  });

  it("marks the pull request as newly opened", () => {
    const s = buildTriggerSample("github_on_new_pr") as Record<string, unknown>;
    expect(s.action).toBe("opened");
    const pr = s.pull_request as Record<string, unknown>;
    expect(pr.number).toBeTypeOf("number");
    expect((pr.user as Record<string, unknown>).login).toBeTruthy();
    expect((pr.head as Record<string, unknown>).ref).toBeTruthy();
  });

  it("gives a push a commit list, not just a ref", () => {
    const s = buildTriggerSample("github_on_push") as Record<string, unknown>;
    expect(s.ref).toContain("refs/heads/");
    expect(Array.isArray(s.commits)).toBe(true);
    expect((s.commits as unknown[]).length).toBeGreaterThan(0);
  });

  it("only claims the triggers the daemon can actually seed", () => {
    expect(canTestFire("slack_on_mention")).toBe(true);
    expect(canTestFire("webhook_input")).toBe(true);
    // A schedule trigger derives its own fire moment; plain Run covers it.
    expect(canTestFire("cron_trigger")).toBe(false);
    expect(canTestFire("poll_trigger")).toBe(false);
    expect(canTestFire(undefined)).toBe(false);
    expect(TEST_TRIGGER_MODULES.size).toBe(6);
  });
});
