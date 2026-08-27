// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// Stable `t` and auth: the page's load callback lists `t` in its deps, so a
// fresh function per render would re-fire it forever.
vi.mock("react-i18next", () => {
  const t = (k: string, o?: Record<string, unknown>) =>
    o && typeof o === "object" ? `${k}:${JSON.stringify(o)}` : k;
  const value = { t };
  return {
    useTranslation: () => value,
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../../auth", () => {
  const auth = {
    token: "tok",
    me: { tenant: "t", workspace: "ws" },
    hasPerm: () => true,
  };
  return { useAuth: () => auth };
});

const listWebAPIs = vi.fn();
const saveWebAPI = vi.fn();
const deleteWebAPI = vi.fn();
const webAPIUsage = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    listWebAPIs: (...a: unknown[]) => listWebAPIs(...a),
    saveWebAPI: (...a: unknown[]) => saveWebAPI(...a),
    deleteWebAPI: (...a: unknown[]) => deleteWebAPI(...a),
    webAPIUsage: (...a: unknown[]) => webAPIUsage(...a),
  },
}));

import { AdminWebAPIs } from "./AdminWebAPIs";

const orders = {
  name: "order-service",
  label: "Order service",
  base_url: "https://api.example.com/v1",
  auth_kind: "bearer" as const,
  operations: [
    {
      id: "get_order",
      method: "GET" as const,
      path: "/orders/{order_id}",
      summary: "Fetch one order",
      args: [
        {
          name: "order_id",
          in: "path" as const,
          type: "string",
          required: true,
        },
      ],
    },
  ],
  enabled: true,
  registered: true,
  step_ids: [
    "api:order-service:get_order",
    "api:order-service:list_orders",
    "api:order-service:create_order",
    "api:order-service:cancel_order",
  ],
  created_at: "2026-08-01T10:00:00Z",
  updated_at: "2026-08-01T10:00:00Z",
};

beforeEach(() => {
  vi.clearAllMocks();
  listWebAPIs.mockResolvedValue({ web_apis: [orders] });
  webAPIUsage.mockResolvedValue({ flows: [], hidden: 0 });
});

describe("AdminWebAPIs", () => {
  it("names the steps the catalog contributed, not just how many", async () => {
    render(<AdminWebAPIs />);
    await waitFor(() =>
      expect(screen.getByText("Order service")).toBeInTheDocument(),
    );
    // The operation ids, so an admin knows what to search the palette for.
    expect(
      screen.getByText(/get_order, list_orders, create_order/),
    ).toBeInTheDocument();
    expect(screen.getByText(/webapi.andMore/)).toBeInTheDocument();
    // And the id, which is what flow JSON holds.
    expect(screen.getByText("order-service")).toBeInTheDocument();
  });

  // The status must not claim the service is reachable: nothing was dialed.
  it("reports being in the palette, never being connected", async () => {
    render(<AdminWebAPIs />);
    await waitFor(() =>
      expect(screen.getByText("webapi.inPalette")).toBeInTheDocument(),
    );
    expect(screen.queryByText(/connected/i)).not.toBeInTheDocument();
  });

  it("shows a stored catalog that could not be loaded as needing attention", async () => {
    listWebAPIs.mockResolvedValue({
      web_apis: [
        { ...orders, registered: false, last_error: "path names {missing}" },
      ],
    });
    render(<AdminWebAPIs />);
    await waitFor(() =>
      expect(screen.getByText("webapi.broken")).toBeInTheDocument(),
    );
  });

  it("shows a turned-off catalog as off rather than as broken", async () => {
    listWebAPIs.mockResolvedValue({
      web_apis: [{ ...orders, enabled: false, registered: false }],
    });
    render(<AdminWebAPIs />);
    await waitFor(() =>
      expect(screen.getByText("webapi.disabled")).toBeInTheDocument(),
    );
    expect(screen.queryByText("webapi.broken")).not.toBeInTheDocument();
  });

  // The whole point of the editor: an operation and its typed arguments reach
  // the API in the shape the daemon validates.
  it("submits the described operation with its arguments", async () => {
    listWebAPIs.mockResolvedValue({ web_apis: [] });
    saveWebAPI.mockResolvedValue({ ...orders });
    render(<AdminWebAPIs />);
    await waitFor(() =>
      expect(screen.getByText("webapi.emptyTitle")).toBeInTheDocument(),
    );

    await userEvent.click(screen.getByText("webapi.add"));
    await userEvent.type(screen.getByLabelText("common.name"), "Order service");
    await userEvent.type(
      screen.getByLabelText("webapi.urlLabel"),
      "https://api.example.com/v1",
    );
    await userEvent.type(
      screen.getByLabelText("webapi.opIdLabel"),
      "get_order",
    );
    const path = screen.getByLabelText("webapi.opPathLabel");
    await userEvent.clear(path);
    // `{` opens a key descriptor in user-event, so a literal one is doubled.
    await userEvent.type(path, "/orders/{{order_id}");
    await userEvent.type(
      screen.getByLabelText("webapi.opSummaryLabel"),
      "Fetch one order",
    );

    await userEvent.click(screen.getByText(/webapi.addArgument/));
    await userEvent.type(
      screen.getByLabelText("webapi.argNameLabel"),
      "order_id",
    );
    await userEvent.selectOptions(
      screen.getByLabelText("webapi.argInLabel"),
      "path",
    );
    await userEvent.click(screen.getByLabelText("webapi.argRequired"));

    await userEvent.click(screen.getByText("webapi.save"));

    await waitFor(() => expect(saveWebAPI).toHaveBeenCalled());
    const [, input, existing] = saveWebAPI.mock.calls[0];
    expect(existing).toBeUndefined(); // a create: the daemon derives the id
    expect(input.label).toBe("Order service");
    expect(input.base_url).toBe("https://api.example.com/v1");
    expect(input.operations).toHaveLength(1);
    expect(input.operations[0]).toMatchObject({
      id: "get_order",
      method: "GET",
      path: "/orders/{order_id}",
      summary: "Fetch one order",
    });
    expect(input.operations[0].args[0]).toMatchObject({
      name: "order_id",
      in: "path",
      type: "string",
      required: true,
    });
  });

  // An edit sends the existing name, so the daemon replaces rather than creating
  // a second catalog with a numbered id.
  it("edits in place rather than creating a second catalog", async () => {
    saveWebAPI.mockResolvedValue({ ...orders });
    render(<AdminWebAPIs />);
    await waitFor(() =>
      expect(screen.getByText("Order service")).toBeInTheDocument(),
    );
    await userEvent.click(screen.getByLabelText("common.edit"));
    await userEvent.click(screen.getByText("webapi.save"));
    await waitFor(() => expect(saveWebAPI).toHaveBeenCalled());
    expect(saveWebAPI.mock.calls[0][2]).toBe("order-service");
  });

  // A body argument is only legal when the operation sends a JSON body, so the
  // choice appears only then — and switching away relocates the argument instead
  // of dropping what the admin typed.
  it("offers a body argument only for a JSON body, and rehomes it when that changes", async () => {
    listWebAPIs.mockResolvedValue({ web_apis: [] });
    render(<AdminWebAPIs />);
    await waitFor(() =>
      expect(screen.getByText("webapi.emptyTitle")).toBeInTheDocument(),
    );
    await userEvent.click(screen.getByText("webapi.add"));

    await userEvent.click(screen.getByText(/webapi.addArgument/));
    expect(
      screen.queryByRole("option", { name: "webapi.inBody" }),
    ).not.toBeInTheDocument();

    await userEvent.selectOptions(
      screen.getByLabelText("webapi.opBodyLabel"),
      "json",
    );
    await userEvent.selectOptions(
      screen.getByLabelText("webapi.argInLabel"),
      "body",
    );
    expect(
      (screen.getByLabelText("webapi.argInLabel") as HTMLSelectElement).value,
    ).toBe("body");

    await userEvent.selectOptions(
      screen.getByLabelText("webapi.opBodyLabel"),
      "none",
    );
    expect(
      (screen.getByLabelText("webapi.argInLabel") as HTMLSelectElement).value,
    ).toBe("query");
  });

  it("names the flows that will break when removing a catalog in use", async () => {
    webAPIUsage.mockResolvedValue({
      flows: [
        {
          workspace: "ws1",
          flow_id: "invoices",
          name: "Nightly invoices",
          steps: ["api:order-service:create_order"],
          published: true,
        },
      ],
      hidden: 0,
    });
    render(<AdminWebAPIs />);
    await screen.findByText("Order service");
    await userEvent.click(screen.getByLabelText("common.remove"));

    const warning = await screen.findByText(/webapi.removeInUse/);
    expect(warning.textContent).toContain("Nightly invoices");
    expect(warning.textContent).toContain("webapi.removePublished");
    await waitFor(() => expect(webAPIUsage).toHaveBeenCalledWith("tok", "order-service"));
  });

  it("says plainly when nothing uses the catalog", async () => {
    render(<AdminWebAPIs />);
    await screen.findByText("Order service");
    await userEvent.click(screen.getByLabelText("common.remove"));

    expect(await screen.findByText(/webapi.removeUnused/)).toBeInTheDocument();
    expect(screen.queryByText(/webapi.removeInUse/)).toBeNull();
  });

  it("still warns when the usage lookup fails, rather than claiming safety", async () => {
    webAPIUsage.mockRejectedValue(new Error("boom"));
    deleteWebAPI.mockResolvedValue({ deleted: "order-service" });
    render(<AdminWebAPIs />);
    await screen.findByText("Order service");
    await userEvent.click(screen.getByLabelText("common.remove"));

    expect(await screen.findByText(/webapi.removeReally/)).toBeInTheDocument();
    expect(screen.queryByText(/webapi.removeUnused/)).toBeNull();
    // And the delete still works.
    await userEvent.click(screen.getByText("common.remove"));
    await waitFor(() => expect(deleteWebAPI).toHaveBeenCalledWith("tok", "order-service"));
  });

  it("confirms before removing, because flows stop running", async () => {
    deleteWebAPI.mockResolvedValue({ deleted: "order-service" });
    render(<AdminWebAPIs />);
    await waitFor(() =>
      expect(screen.getByText("Order service")).toBeInTheDocument(),
    );
    await userEvent.click(screen.getByLabelText("common.remove"));
    expect(deleteWebAPI).not.toHaveBeenCalled();
    expect(screen.getByText(/webapi.remove(Really|Unused)/)).toBeInTheDocument();
    await userEvent.click(screen.getByText("common.remove"));
    await waitFor(() =>
      expect(deleteWebAPI).toHaveBeenCalledWith("tok", "order-service"),
    );
  });
});
