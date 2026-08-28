// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

// Stable `t` and auth, as the admin page tests do: the load effect lists `t` in
// its deps, so a fresh function per render would re-fire it forever.
vi.mock("react-i18next", () => {
  const t = (k: string, o?: Record<string, unknown>) =>
    o && typeof o === "object" ? `${k}:${JSON.stringify(o)}` : k;
  const value = { t, i18n: { language: "en" } };
  return {
    useTranslation: () => value,
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../auth", () => {
  const auth = { token: "tok", me: { tenant: "t", workspace: "ws" }, hasPerm: () => true };
  return { useAuth: () => auth };
});

const listDrops = vi.fn();
const listSecrets = vi.fn();
const listProviders = vi.fn();
vi.mock("../api", () => ({
  APIError: class extends Error {},
  api: {
    listDrops: (...a: unknown[]) => listDrops(...a),
    listSecrets: (...a: unknown[]) => listSecrets(...a),
    listProviders: (...a: unknown[]) => listProviders(...a),
  },
}));

import { Apps } from "./Apps";

// drop builds one manifest. Only the fields the index reads are set.
function drop(id: string, integration: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    version: "1.0",
    label: id,
    integration,
    category: "network",
    inputs: [],
    outputs: [],
    ...extra,
  };
}

// A catalog big enough to page: 30 apps named App 01 … App 30, one step each.
function manyApps(n: number) {
  return Array.from({ length: n }, (_, i) =>
    drop(`step_${i}`, `App ${String(i + 1).padStart(2, "0")}`),
  );
}

function renderApps(initial = "/apps") {
  return render(
    <MemoryRouter initialEntries={[initial]}>
      <Apps />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  listSecrets.mockResolvedValue({ secrets: [] });
  listProviders.mockResolvedValue({ providers: [] });
  listDrops.mockResolvedValue({ drops: manyApps(30) });
});

describe("Apps index", () => {
  // The point of the rewrite: a thousand-app catalog must not lay out a
  // thousand cards.
  it("shows one page of apps and says which slice that is", async () => {
    renderApps();
    await waitFor(() => expect(screen.getByText("App 01")).toBeInTheDocument());

    expect(screen.getByText("App 24")).toBeInTheDocument();
    expect(screen.queryByText("App 25")).not.toBeInTheDocument();
    expect(
      screen.getByText(/integrations.countRange.*"from":1,"to":24,"total":30/),
    ).toBeInTheDocument();
  });

  it("pages forward and back", async () => {
    renderApps();
    await waitFor(() => expect(screen.getByText("App 01")).toBeInTheDocument());

    await userEvent.click(screen.getByText("integrations.pageNext"));
    await waitFor(() => expect(screen.getByText("App 25")).toBeInTheDocument());
    expect(screen.queryByText("App 01")).not.toBeInTheDocument();
    expect(
      screen.getByText(/integrations.countRange.*"from":25,"to":30,"total":30/),
    ).toBeInTheDocument();

    await userEvent.click(screen.getByText("integrations.pagePrev"));
    await waitFor(() => expect(screen.getByText("App 01")).toBeInTheDocument());
  });

  // A stale ?page= (a bookmark, or a hand-edited URL) must show the last page of
  // results rather than an empty grid.
  it("clamps a page number past the end", async () => {
    renderApps("/apps?page=99");
    await waitFor(() => expect(screen.getByText("App 25")).toBeInTheDocument());
    expect(screen.queryByText("App 01")).not.toBeInTheDocument();
  });

  it("has no pager when everything fits on one page", async () => {
    listDrops.mockResolvedValue({ drops: manyApps(3) });
    renderApps();
    await waitFor(() => expect(screen.getByText("App 01")).toBeInTheDocument());
    expect(screen.queryByText("integrations.pageNext")).not.toBeInTheDocument();
  });

  it("filters by search text", async () => {
    renderApps();
    await waitFor(() => expect(screen.getByText("App 01")).toBeInTheDocument());

    await userEvent.type(screen.getByLabelText("integrations.searchPlaceholder"), "App 17");
    await waitFor(() => expect(screen.queryByText("App 01")).not.toBeInTheDocument());
    expect(screen.getByText("App 17")).toBeInTheDocument();
    expect(
      screen.getByText(/integrations.countRange.*"from":1,"to":1,"total":1/),
    ).toBeInTheDocument();
  });

  // The difference between a search box and a useful one: the steps inside an
  // app are searchable, because nobody knows the SMS app is called 46elks.
  it("finds an app by the name of a step inside it", async () => {
    listDrops.mockResolvedValue({
      drops: [drop("elks_send_sms", "46elks", { label: "Send an SMS" })],
    });
    renderApps();
    await waitFor(() => expect(screen.getByText("46elks")).toBeInTheDocument());

    await userEvent.type(screen.getByLabelText("integrations.searchPlaceholder"), "sms");
    await waitFor(() =>
      expect(
        screen.getByText(/integrations.countRange.*"from":1,"to":1,"total":1/),
      ).toBeInTheDocument(),
    );
    expect(screen.getByText("46elks")).toBeInTheDocument();
  });

  // An app an ORG created has no curated entry in integrationMeta and never
  // will — the table lives in this repo. Its own steps carry the prose, so the
  // card reads like every other card.
  it("shows an uncurated app's own description", async () => {
    listDrops.mockResolvedValue({
      drops: [
        drop("api:order-service:get_order", "Order service", {
          label: "Fetch an order",
          integration_description: "Our order system, in our own words.",
        }),
      ],
    });
    renderApps();
    expect(
      await screen.findByText("Our order system, in our own words."),
    ).toBeInTheDocument();
  });

  // The slug round trip is lossy, so the name comes off the manifest: an admin
  // who typed "Order service" must not find "Order Service" on the page.
  it("names an uncurated app the way it was typed", async () => {
    listDrops.mockResolvedValue({
      drops: [drop("api:order-service:get_order", "Order service")],
    });
    renderApps();
    expect(await screen.findByText("Order service")).toBeInTheDocument();
    expect(screen.queryByText("Order Service")).toBeNull();
  });

  // And it is searchable by it, which is the other half of a description
  // earning its place on a page built to be read at catalog scale.
  it("finds an uncurated app by its own description", async () => {
    listDrops.mockResolvedValue({
      drops: [
        drop("api:order-service:get_order", "Order service", {
          label: "Fetch an order",
          integration_description: "Warehouse picking and dispatch.",
        }),
        ...manyApps(5),
      ],
    });
    renderApps();
    await waitFor(() =>
      expect(screen.getByText("Order service")).toBeInTheDocument(),
    );
    await userEvent.type(
      screen.getByLabelText("integrations.searchPlaceholder"),
      "dispatch",
    );
    await waitFor(() =>
      expect(screen.queryByText("App 01")).not.toBeInTheDocument(),
    );
    expect(screen.getByText("Order service")).toBeInTheDocument();
  });

  // Diacritics fold, so a user types what their keyboard has.
  it("matches across diacritics", async () => {
    listDrops.mockResolvedValue({ drops: [drop("fortnox_x", "Bokföring")] });
    renderApps();
    await waitFor(() => expect(screen.getByText("Bokföring")).toBeInTheDocument());
    await userEvent.type(screen.getByLabelText("integrations.searchPlaceholder"), "bokforing");
    await waitFor(() =>
      expect(
        screen.getByText(/integrations.countRange.*"total":1/),
      ).toBeInTheDocument(),
    );
  });

  // Narrowing the results while on page 2 must not leave the user on an empty
  // page — the classic paginated-filter bug.
  it("returns to the first page when a filter changes", async () => {
    renderApps();
    await waitFor(() => expect(screen.getByText("App 01")).toBeInTheDocument());
    await userEvent.click(screen.getByText("integrations.pageNext"));
    await waitFor(() => expect(screen.getByText("App 25")).toBeInTheDocument());

    await userEvent.type(screen.getByLabelText("integrations.searchPlaceholder"), "App");
    await waitFor(() => expect(screen.getByText("App 01")).toBeInTheDocument());
  });

  it("filters by connection state", async () => {
    listDrops.mockResolvedValue({
      drops: [
        drop("needs_a", "Needy", {
          requires_connections: [{ kind: "secret", name: "NEEDY_KEY" }],
        }),
        drop("plain_a", "Plain"),
      ],
    });
    renderApps();
    await waitFor(() => expect(screen.getByText("Needy")).toBeInTheDocument());

    await userEvent.selectOptions(screen.getByLabelText("common.status", { exact: false }), [
      "needs_setup",
    ]);
    await waitFor(() => expect(screen.queryByText("Plain")).not.toBeInTheDocument());
    expect(screen.getByText("Needy")).toBeInTheDocument();
  });

  // The pre-existing deep link (?category=ai, from "Connect an AI provider")
  // has to keep working: it is the same parameter the dropdown now writes.
  it("honours a ?category= deep link", async () => {
    listDrops.mockResolvedValue({
      drops: [
        drop("claude_x", "Anthropic", { category: "ai" }),
        drop("http_x", "HTTP", { category: "network" }),
      ],
    });
    renderApps("/apps?category=ai");
    await waitFor(() => expect(screen.getByText("Anthropic")).toBeInTheDocument());
    expect(screen.queryByText("HTTP")).not.toBeInTheDocument();
  });

  it("offers a way out when nothing matches", async () => {
    renderApps();
    await waitFor(() => expect(screen.getByText("App 01")).toBeInTheDocument());
    await userEvent.type(
      screen.getByLabelText("integrations.searchPlaceholder"),
      "nothing matches this",
    );
    await waitFor(() =>
      expect(screen.getByText("integrations.noResultsTitle")).toBeInTheDocument(),
    );
    expect(screen.getByText("integrations.countNone")).toBeInTheDocument();

    // Two of them: one in the toolbar, one in the empty state.
    const clears = screen.getAllByText("integrations.filterClear");
    await userEvent.click(clears[clears.length - 1]);
    await waitFor(() => expect(screen.getByText("App 01")).toBeInTheDocument());
  });

  // Each card carries the state the section headings used to carry, now that the
  // grid is flat.
  it("names each app's state on its card", async () => {
    listDrops.mockResolvedValue({
      drops: [
        drop("needs_a", "Needy", {
          requires_connections: [{ kind: "secret", name: "NEEDY_KEY" }],
        }),
        drop("done_a", "Done", {
          requires_connections: [{ kind: "secret", name: "DONE_KEY" }],
        }),
      ],
    });
    listSecrets.mockResolvedValue({ secrets: ["DONE_KEY"] });
    renderApps();
    await waitFor(() => expect(screen.getByText("Done")).toBeInTheDocument());

    const done = screen.getByText("Done").closest(".integration-card") as HTMLElement;
    expect(within(done).getByText("integrations.connectedTip")).toBeInTheDocument();
    const needy = screen.getByText("Needy").closest(".integration-card") as HTMLElement;
    expect(within(needy).getByText("integrations.needsSetupHead")).toBeInTheDocument();
  });
});
