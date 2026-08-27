// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// The gallery's own wiring. templateText.test.ts proves the vocabulary
// resolves; this proves the cards actually ask it, which is the half that was
// missing — every title, one-liner and heading rendered straight off the wire.
import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

// language is read by the mocked useTranslation below, so a test can re-render
// the same gallery as a Swedish reader.
let language = "en";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) =>
      o && typeof o === "object" ? `${k}:${JSON.stringify(o)}` : k,
    get i18n() {
      return { language };
    },
  }),
}));

vi.mock("../auth", () => ({
  useAuth: () => ({
    token: "tok",
    activeTenant: "t",
    activeWorkspace: "ws",
    hasPerm: () => true,
  }),
}));

const listTemplates = vi.fn();
const listProviders = vi.fn();
vi.mock("../api", () => ({
  api: {
    listTemplates: () => listTemplates(),
    listProviders: (...a: unknown[]) => listProviders(...a),
  },
}));

import { TemplateGallery } from "./TemplateGallery";

// The real index entry, so the fingerprints under test are the shipped ones.
const emailToSlack = {
  id: "email-to-slack",
  title: "New email → Slack message",
  use_case:
    "Get a Slack message for each new email so your team sees it in chat without watching the inbox.",
  category: "Notifications",
  description: "A technical summary nobody sees on the card.",
  graph_file: "email-to-slack.json",
};

beforeEach(() => {
  vi.clearAllMocks();
  language = "en";
  listTemplates.mockResolvedValue({ templates: [emailToSlack] });
  listProviders.mockResolvedValue({ providers: [] });
});

const renderGallery = () =>
  render(
    <MemoryRouter>
      <TemplateGallery />
    </MemoryRouter>,
  );

describe("TemplateGallery", () => {
  it("renders the English as authored", async () => {
    renderGallery();
    expect(await screen.findByText(emailToSlack.title)).toBeInTheDocument();
    expect(screen.getByText(emailToSlack.use_case)).toBeInTheDocument();
    expect(screen.getByText("Notifications")).toBeInTheDocument();
  });

  // The bug: a Swedish reader got Swedish buttons over English cards.
  it("renders the card and its heading in the reader's language", async () => {
    language = "sv";
    renderGallery();
    expect(
      await screen.findByText("Ny e-post → Slack-meddelande"),
    ).toBeInTheDocument();
    expect(screen.queryByText(emailToSlack.title)).toBeNull();
    expect(screen.getByText("Aviseringar")).toBeInTheDocument();
    expect(screen.queryByText("Notifications")).toBeNull();
  });

  // Grouping keys stay English, so which cards sit together — and the
  // ?category= link that reproduces it — do not depend on the language.
  it("groups by the English category whatever the language", async () => {
    language = "sv";
    listTemplates.mockResolvedValue({
      templates: [emailToSlack, { ...emailToSlack, id: "second" }],
    });
    renderGallery();
    await screen.findAllByText("Ny e-post → Slack-meddelande");
    expect(screen.getAllByText("Aviseringar")).toHaveLength(1);
  });

  // A template with no category falls in the catch-all bucket, whose heading is
  // an ordinary i18n key rather than server prose.
  it("names the catch-all bucket from the app's own strings", async () => {
    listTemplates.mockResolvedValue({
      templates: [{ ...emailToSlack, category: undefined }],
    });
    renderGallery();
    expect(await screen.findByText("templates.uncategorized")).toBeInTheDocument();
  });
});
