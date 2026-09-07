// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The reachability lines above the Webhook and Form config, and the one thing
// neither may do: claim a door works when it doesn't.
//
// The form line sits directly above the form URL and its Copy button, so it is
// read at the exact moment an owner decides whether to send that link to a
// customer. Both answer about the PUBLISHED flow, because /trigger and /form
// both serve that — answering from the draft went green immediately while
// every visitor still got a 404.

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
vi.mock("../../i18n", () => ({
  default: { language: "en", t: (k: string) => k },
}));

import {
  FormStatusLine,
  RequestStatusLine,
  WebhookStatusLine,
} from "./TriggersModal";
import type { GraphTrigger } from "../../types";

const keyed = { secrets: ["s"] } as unknown as GraphTrigger;
const bare = {} as GraphTrigger;

const line = (kind: string) =>
  screen.getByText(new RegExp(`^inspector\\.${kind}\\.`)).textContent;
// classList, not a substring match: "webhook-status" literally contains "ok".
const classes = (c: HTMLElement) =>
  c.querySelector(".webhook-status")!.classList;

describe("WebhookStatusLine", () => {
  it("reports no door at all until a key exists", () => {
    const { container } = render(
      <WebhookStatusLine
        webhook={bare}
        triggerLive={{ published: true, dirty: false }}
      />,
    );
    expect(line("webhookStatus")).toBe("inspector.webhookStatus.off");
    expect(classes(container).contains("ok")).toBe(false);
  });

  it("does not promise a working key on an unpublished draft", () => {
    const { container } = render(
      <WebhookStatusLine
        webhook={keyed}
        triggerLive={{ published: false, dirty: true }}
      />,
    );
    expect(line("webhookStatus")).toBe("inspector.webhookStatus.pending");
    expect(classes(container).contains("ok")).toBe(false);
  });

  it("goes green once published and clean", () => {
    const { container } = render(
      <WebhookStatusLine
        webhook={keyed}
        triggerLive={{ published: true, dirty: false }}
      />,
    );
    expect(line("webhookStatus")).toBe("inspector.webhookStatus.on");
    expect(classes(container).contains("ok")).toBe(true);
  });

  it("warns that senders still reach the last published version", () => {
    // Published, with edits on top: the door works, but it leads to the
    // version on the server, not the one on screen. Still green — a sender
    // CAN use it right now — with the stale marker on top.
    const { container } = render(
      <WebhookStatusLine
        webhook={keyed}
        triggerLive={{ published: true, dirty: true }}
      />,
    );
    expect(line("webhookStatus")).toBe("inspector.webhookStatus.stale");
    expect(classes(container).contains("ok")).toBe(true);
    expect(classes(container).contains("stale")).toBe(true);
  });

  it("stays on the door-only answer while publish state is unknown", () => {
    // Still loading, or a surface that doesn't pass it. Saying "not published"
    // here would be a guess, and a wrong one most of the time.
    render(<WebhookStatusLine webhook={keyed} />);
    expect(line("webhookStatus")).toBe("inspector.webhookStatus.on");
  });
});

describe("FormStatusLine", () => {
  it("does not promise a working link on an unpublished draft", () => {
    // The one thing this line must never do: an owner reads it and sends the
    // link to a customer, who gets "not available" until the flow is live.
    const { container } = render(
      <FormStatusLine triggerLive={{ published: false, dirty: true }} />,
    );
    expect(line("formStatus")).toBe("inspector.formStatus.pending");
    expect(classes(container).contains("ok")).toBe(false);
  });

  it("is open the moment the flow is published — a form needs no key", () => {
    const { container } = render(
      <FormStatusLine triggerLive={{ published: true, dirty: false }} />,
    );
    expect(line("formStatus")).toBe("inspector.formStatus.on");
    expect(classes(container).contains("ok")).toBe(true);
  });

  it("warns that visitors still get the last published version", () => {
    const { container } = render(
      <FormStatusLine triggerLive={{ published: true, dirty: true }} />,
    );
    expect(line("formStatus")).toBe("inspector.formStatus.stale");
    expect(classes(container).contains("ok")).toBe(true);
    expect(classes(container).contains("stale")).toBe(true);
  });
});

// A step the author opened is receiving. Saying "press Generate" over an
// endpoint the whole internet can already POST to would be the most misleading
// line on the page.
describe("an open webhook step", () => {
  const open = { public: true } as unknown as GraphTrigger;

  it("reads as receiving, not as unconfigured", () => {
    const { container } = render(
      <WebhookStatusLine
        webhook={open}
        triggerLive={{ published: true, dirty: false }}
      />,
    );
    expect(line("webhookStatus")).toBe("inspector.webhookStatus.open");
    expect(classes(container).contains("ok")).toBe(true);
  });

  it("still waits on publish like any other door", () => {
    render(
      <WebhookStatusLine
        webhook={open}
        triggerLive={{ published: false, dirty: false }}
      />,
    );
    expect(line("webhookStatus")).toBe("inspector.webhookStatus.pending");
  });

  it("is off when the switch is off and there is no key", () => {
    render(
      <WebhookStatusLine
        webhook={bare}
        triggerLive={{ published: true, dirty: false }}
      />,
    );
    expect(line("webhookStatus")).toBe("inspector.webhookStatus.off");
  });
});

// The Request step gets the same two doors, and the same duty not to claim a
// closed one works — with its own words, because /call answers.
describe("an open Request step", () => {
  const open = { public: true } as unknown as GraphTrigger;

  it("reads as answering, not as unconfigured", () => {
    const { container } = render(
      <RequestStatusLine
        request={open}
        triggerLive={{ published: true, dirty: false }}
      />,
    );
    expect(line("requestStatus")).toBe("inspector.requestStatus.open");
    expect(classes(container).contains("ok")).toBe(true);
  });

  it("is off when the switch is off and there is no key", () => {
    render(
      <RequestStatusLine
        request={bare}
        triggerLive={{ published: true, dirty: false }}
      />,
    );
    expect(line("requestStatus")).toBe("inspector.requestStatus.off");
  });
});
