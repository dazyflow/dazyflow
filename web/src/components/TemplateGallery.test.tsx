// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The gallery's own wiring. templateText.test.ts proves the vocabulary
// resolves; this proves the cards actually ask it, which is the half that was
// missing — every title, one-liner and heading rendered straight off the wire.
import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
const loadTemplateGraph = vi.fn();
const saveGraph = vi.fn();
vi.mock("../api", () => ({
  api: {
    listTemplates: () => listTemplates(),
    listProviders: (...a: unknown[]) => listProviders(...a),
    loadTemplateGraph: (...a: unknown[]) => loadTemplateGraph(...a),
    saveGraph: (...a: unknown[]) => saveGraph(...a),
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
  loadTemplateGraph.mockResolvedValue({ id: "email-to-slack", nodes: [], edges: [] });
  saveGraph.mockResolvedValue({});
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

// The forked flow's OUTPUT language — what its hosted form says to visitors —
// used to be left empty, which means English. A Swedish owner forking the
// form template therefore published an English form ("Submit", "Thanks! Your
// submission was received.") to Swedish customers, having never been shown a
// language control. The fork is where the other per-owner details are stamped
// (time zone, ntfy topic), so it is where this belongs too.
describe("TemplateGallery forking stamps the flow's language", () => {
  const forkedGraph = async () => {
    const user = userEvent.setup();
    renderGallery();
    await user.click(await screen.findByText("templates.useTemplate"));
    await waitFor(() => expect(saveGraph).toHaveBeenCalled());
    return saveGraph.mock.calls[0][1] as { language?: string };
  };

  it("stamps the forker's language, without the region", async () => {
    language = "sv-SE";
    expect((await forkedGraph()).language).toBe("sv");
  });

  it("stamps English for an English reader", async () => {
    language = "en-US";
    expect((await forkedGraph()).language).toBe("en");
  });

  it("keeps a language the template chose for itself", async () => {
    // A template written to produce Swedish output stays Swedish even when an
    // English speaker forks it — the author picked it deliberately.
    language = "en";
    loadTemplateGraph.mockResolvedValue({
      id: "email-to-slack",
      nodes: [],
      edges: [],
      language: "sv",
    });
    expect((await forkedGraph()).language).toBe("sv");
  });
});

// The card the user clicked and the flow they end up with should be the same
// thing. Titles render through templateTitle(), so a Swedish reader picked
// "Webbformulär → Samling" and landed on a flow called "Web form → Collection"
// — the graph file's raw English name, which the fork copied straight through.
describe("TemplateGallery names the fork after the card", () => {
  const forkedGraph = async () => {
    const user = userEvent.setup();
    renderGallery();
    await user.click(await screen.findByText("templates.useTemplate"));
    await waitFor(() => expect(saveGraph).toHaveBeenCalled());
    return saveGraph.mock.calls[0][1] as { name?: string };
  };

  it("uses the English title for an English reader", async () => {
    language = "en";
    expect((await forkedGraph()).name).toBe("New email → Slack message");
  });

  it("uses the translated title the card showed", async () => {
    language = "sv";
    const name = (await forkedGraph()).name;
    // Whatever the Swedish vocabulary carries — the point is that it is the
    // card's text, not the graph file's English.
    expect(name).toBe(screen.getByRole("heading", { level: 3 }).textContent);
  });
});
