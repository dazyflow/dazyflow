// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The reachability line above the webhook config, and the one thing it must
// never do: claim a form link works when it doesn't.
//
// It sits directly above the form URL and its Copy button, so it is read at
// the exact moment an owner decides whether to send that link to a customer.
// It used to answer from the DRAFT alone — turn the hosted form on and it went
// green immediately, while /form served the published revision and answered
// every visitor with a 404 until the flow was published.

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  const value = { t, i18n: { language: "en" } };
  return {
    useTranslation: () => value,
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../../i18n", () => ({ default: { language: "en", t: (k: string) => k } }));

import { WebhookStatusLine } from "./TriggersModal";
import type { GraphTrigger } from "../../types";

const form = { public_form: true } as GraphTrigger;
const secret = { secrets: ["s"] } as unknown as GraphTrigger;
const both = { public_form: true, secrets: ["s"] } as unknown as GraphTrigger;

const line = () => screen.getByText(/^inspector\.webhookStatus\./).textContent;
// classList, not a substring match: "webhook-status" literally contains "ok".
const classes = (c: HTMLElement) => c.querySelector(".webhook-status")!.classList;

describe("WebhookStatusLine", () => {
  it("does not promise a working form link on an unpublished draft", () => {
    const { container } = render(
      <WebhookStatusLine webhook={form} triggerLive={{ published: false, dirty: true }} />,
    );
    expect(line()).toBe("inspector.webhookStatus.pending.form");
    // Green is the signal an owner reads as "safe to send". It must be absent
    // until a stranger with the link would actually get a form.
    expect(classes(container).contains("ok")).toBe(false);
  });

  it("names the secret key when that is the only door", () => {
    render(
      <WebhookStatusLine webhook={secret} triggerLive={{ published: false, dirty: false }} />,
    );
    expect(line()).toBe("inspector.webhookStatus.pending.secret");
  });

  it("names both doors when both are configured", () => {
    render(
      <WebhookStatusLine webhook={both} triggerLive={{ published: false, dirty: false }} />,
    );
    expect(line()).toBe("inspector.webhookStatus.pending.both");
  });

  it("goes green once published and clean", () => {
    const { container } = render(
      <WebhookStatusLine webhook={form} triggerLive={{ published: true, dirty: false }} />,
    );
    expect(line()).toBe("inspector.webhookStatus.form");
    expect(classes(container).contains("ok")).toBe(true);
  });

  it("warns that visitors still get the last published version", () => {
    // Published, with edits on top: the link works, but the fields someone is
    // filling in are not the ones on screen. Still green — a stranger CAN use
    // it right now — with the stale marker on top.
    const { container } = render(
      <WebhookStatusLine webhook={form} triggerLive={{ published: true, dirty: true }} />,
    );
    expect(line()).toBe("inspector.webhookStatus.stale");
    expect(classes(container).contains("ok")).toBe(true);
    expect(classes(container).contains("stale")).toBe(true);
  });

  it("stays on the door-only answer while publish state is unknown", () => {
    // Still loading, or a surface that doesn't pass it. Saying "not published"
    // here would be a guess, and a wrong one most of the time.
    render(<WebhookStatusLine webhook={form} />);
    expect(line()).toBe("inspector.webhookStatus.form");
  });

  it("reports no door at all before publish state can matter", () => {
    render(
      <WebhookStatusLine webhook={{} as GraphTrigger} triggerLive={{ published: false, dirty: false }} />,
    );
    expect(line()).toBe("inspector.webhookStatus.off");
  });
});
