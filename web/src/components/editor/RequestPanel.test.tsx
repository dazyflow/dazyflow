// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The Request step's panel. Two things it must get right: the address it hands
// a developer (a Request is NOT a webhook — /call holds the connection, and a
// caller sent to /trigger would get a 202 and never see the flow's Reply), and
// the same publish honesty the webhook line has, since /call also serves the
// published revision.

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
vi.mock("../../auth", () => ({
  useAuth: () => ({ me: { public_base_url: "https://dazy.example" } }),
}));

import { RequestStatusLine, RequestTab } from "./TriggersModal";
import type { Graph, GraphTrigger } from "../../types";

const keyed = { secrets: ["s3cr3t"] } as unknown as GraphTrigger;
const unkeyed = {} as GraphTrigger;
const graph = { id: "ask", tenant: "acme", workspace: "ws1", name: "Ask" } as Graph;

const line = () => screen.getByText(/^inspector\.requestStatus\./).textContent;
const classes = (c: HTMLElement) => c.querySelector(".webhook-status")!.classList;

describe("RequestStatusLine", () => {
  it("says nothing is answering until a key exists", () => {
    const { container } = render(
      <RequestStatusLine request={unkeyed} triggerLive={{ published: true, dirty: false }} />,
    );
    expect(line()).toBe("inspector.requestStatus.off");
    expect(classes(container).contains("ok")).toBe(false);
  });

  it("does not promise answers from an unpublished draft", () => {
    const { container } = render(
      <RequestStatusLine request={keyed} triggerLive={{ published: false, dirty: true }} />,
    );
    expect(line()).toBe("inspector.requestStatus.pending");
    expect(classes(container).contains("ok")).toBe(false);
  });

  it("goes green once a published flow has a key", () => {
    const { container } = render(
      <RequestStatusLine request={keyed} triggerLive={{ published: true, dirty: false }} />,
    );
    expect(line()).toBe("inspector.requestStatus.on");
    expect(classes(container).contains("ok")).toBe(true);
  });
});

describe("RequestTab", () => {
  it("hands out the /call address, never the webhook's /trigger", () => {
    const { container } = render(
      <RequestTab
        graph={graph}
        request={keyed}
        onChange={() => {}}
        triggerLive={{ published: true, dirty: false }}
      />,
    );
    const text = container.textContent ?? "";
    expect(text).toContain("https://dazy.example/call/acme/ws1/ask");
    expect(text).not.toContain("/trigger/");
  });
});
