// SPDX-FileCopyrightText: 2026 Angels' Ware
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
const parseWebAPISpec = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    listWebAPIs: (...a: unknown[]) => listWebAPIs(...a),
    saveWebAPI: (...a: unknown[]) => saveWebAPI(...a),
    deleteWebAPI: (...a: unknown[]) => deleteWebAPI(...a),
    webAPIUsage: (...a: unknown[]) => webAPIUsage(...a),
    parseWebAPISpec: (...a: unknown[]) => parseWebAPISpec(...a),
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
    expect(
      screen.getByText(/get_order, list_orders, create_order/),
    ).toBeInTheDocument();
    expect(screen.getByText(/webapi.andMore/)).toBeInTheDocument();
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

  it("sends runner tags normalised into a list, and warns while they are set", async () => {
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
      "https://orders.internal.example",
    );
    await userEvent.type(screen.getByLabelText("webapi.opIdLabel"), "get_order");

    // Blank is the normal case, so the trade-off notice must not be shown to
    // the many admins who never touch this field.
    expect(screen.queryByText("webapi.runnerTagsWarning")).toBeNull();

    await userEvent.type(
      screen.getByLabelText("webapi.runnerTagsLabel"),
      " orders-box , dmz ",
    );
    expect(screen.getByText("webapi.runnerTagsWarning")).toBeInTheDocument();

    await userEvent.click(screen.getByText("webapi.save"));
    await waitFor(() => expect(saveWebAPI).toHaveBeenCalled());
    const [, input] = saveWebAPI.mock.calls[0];
    expect(input.runner_tags).toEqual(["orders-box", "dmz"]);
  });

  // Omitting runner_tags means "leave it alone" server-side, so a form that
  // cleared the field would be unable to move a catalog back onto a direct
  // call. It is always sent, empty included.
  it("sends an empty tag list when the field is cleared", async () => {
    saveWebAPI.mockResolvedValue({ ...orders });
    render(<AdminWebAPIs />);
    await waitFor(() =>
      expect(screen.getByText("Order service")).toBeInTheDocument(),
    );
    await userEvent.click(screen.getByLabelText("common.edit"));
    await userEvent.click(screen.getByText("webapi.save"));
    await waitFor(() => expect(saveWebAPI).toHaveBeenCalled());
    expect(saveWebAPI.mock.calls[0][1].runner_tags).toEqual([]);
  });

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

  it("offers a name for each operation, so its step is not captioned by an id", async () => {
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
      screen.getByLabelText("webapi.opTitleLabel"),
      "Fetch an order",
    );
    await userEvent.type(screen.getByLabelText("webapi.opIdLabel"), "get_order");
    const path = screen.getByLabelText("webapi.opPathLabel");
    await userEvent.clear(path);
    await userEvent.type(path, "/orders/1");
    await userEvent.click(screen.getByText("webapi.save"));

    await waitFor(() => expect(saveWebAPI).toHaveBeenCalled());
    const [, input] = saveWebAPI.mock.calls[0];
    expect(input.label).toBe("Order service");
    expect(input.operations[0].title).toBe("Fetch an order");
    // A name is sent ALONGSIDE the id, never instead of it: the id is what
    // flows reference and it stays frozen.
    expect(input.operations[0].id).toBe("get_order");
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
    await userEvent.click(screen.getByText("common.remove"));
    await waitFor(() => expect(deleteWebAPI).toHaveBeenCalledWith("tok", "order-service"));
  });

  it("shows the guessed brand mark beside the name, when there is one", async () => {
    const logo = "data:image/png;base64,AAAA";
    listWebAPIs.mockResolvedValue({ web_apis: [{ ...orders, logo }] });
    const { container } = render(<AdminWebAPIs />);
    await screen.findByText("Order service");
    const img = container.querySelector("img.step-source-logo");
    expect(img).toHaveAttribute("src", logo);
    expect(img).toHaveAttribute("alt", "");
  });

  it("renders no mark for a catalog whose favicon was not found", async () => {
    const { container } = render(<AdminWebAPIs />);
    await screen.findByText("Order service");
    expect(container.querySelector("img.step-source-logo")).toBeNull();
  });

  it("edits the service description", async () => {
    listWebAPIs.mockResolvedValue({
      web_apis: [{ ...orders, description: "Our order system." }],
    });
    saveWebAPI.mockResolvedValue(orders);
    render(<AdminWebAPIs />);
    await screen.findByText("Order service");
    await userEvent.click(screen.getByLabelText("common.edit"));

    const field = await screen.findByLabelText("webapi.descriptionLabel");
    expect(field).toHaveValue("Our order system.");
    await userEvent.clear(field);
    await userEvent.type(field, "Warehouse picking and dispatch.");
    await userEvent.click(screen.getByText("webapi.save"));

    await waitFor(() => expect(saveWebAPI).toHaveBeenCalled());
    const input = saveWebAPI.mock.calls[0][1] as { description?: string };
    expect(input.description).toBe("Warehouse picking and dispatch.");
  });

  it("submits the chosen icon source", async () => {
    saveWebAPI.mockResolvedValue({ ...orders, logo_mode: "none" });
    render(<AdminWebAPIs />);
    await screen.findByText("Order service");
    await userEvent.click(screen.getByLabelText("common.edit"));

    await userEvent.selectOptions(
      await screen.findByLabelText("webapi.iconLabel"),
      "none",
    );
    await userEvent.click(screen.getByText("webapi.save"));

    await waitFor(() => expect(saveWebAPI).toHaveBeenCalled());
    const input = saveWebAPI.mock.calls[0][1] as {
      logo_mode?: string;
      logo?: string;
    };
    expect(input.logo_mode).toBe("none");
    expect(input.logo).toBeUndefined();
  });

  // An edit of something else must resend the uploaded mark, or the daemon's
  // "keep what is stored" is the only thing standing between the admin and a
  // lost logo.
  it("resends a custom icon the admin did not change", async () => {
    const logo = "data:image/png;base64,AAAA";
    listWebAPIs.mockResolvedValue({
      web_apis: [{ ...orders, logo, logo_mode: "custom" }],
    });
    saveWebAPI.mockResolvedValue({ ...orders, logo, logo_mode: "custom" });
    render(<AdminWebAPIs />);
    await screen.findByText("Order service");
    await userEvent.click(screen.getByLabelText("common.edit"));
    await screen.findByLabelText("webapi.iconLabel");
    await userEvent.click(screen.getByText("webapi.save"));

    await waitFor(() => expect(saveWebAPI).toHaveBeenCalled());
    const input = saveWebAPI.mock.calls[0][1] as {
      logo_mode?: string;
      logo?: string;
    };
    expect(input).toMatchObject({ logo_mode: "custom", logo });
  });

  it("offers a file to upload only for a chosen image", async () => {
    render(<AdminWebAPIs />);
    await screen.findByText("Order service");
    await userEvent.click(screen.getByLabelText("common.edit"));
    await screen.findByLabelText("webapi.iconLabel");
    expect(screen.queryByLabelText("webapi.iconFileLabel")).toBeNull();

    await userEvent.selectOptions(
      screen.getByLabelText("webapi.iconLabel"),
      "custom",
    );
    expect(screen.getByLabelText("webapi.iconFileLabel")).toBeInTheDocument();
  });

  // A file the picker lets through can still be one we cannot store, and the
  // form has to say which — the alternative is a Save that quietly kept the old
  // mark. (An SVG is chosen here because a type outside `accept` never reaches
  // the change handler at all, in a browser or in this test.)
  it("says why a rejected file was rejected, rather than saving nothing", async () => {
    render(<AdminWebAPIs />);
    await screen.findByText("Order service");
    await userEvent.click(screen.getByLabelText("common.edit"));
    await userEvent.selectOptions(
      await screen.findByLabelText("webapi.iconLabel"),
      "custom",
    );
    const bloated = `<svg xmlns="http://www.w3.org/2000/svg">${"<path d='M0 0'/>".repeat(
      3000,
    )}</svg>`;
    await userEvent.upload(
      screen.getByLabelText("webapi.iconFileLabel"),
      new File([bloated], "logo.svg", { type: "image/svg+xml" }),
    );
    expect(
      await screen.findByText("webapi.icon_svgTooBig"),
    ).toBeInTheDocument();
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


const parsed = {
  title: "Order service",
  base_url: "https://api.example.com/v1",
  max: 60,
  operations: [
    { id: "get_order", method: "GET", path: "/orders/{order_id}", title: "Fetch one order", args: [] },
    { id: "create_order", method: "POST", path: "/orders", title: "Place an order", args: [] },
  ],
};

describe("AdminWebAPIs spec import", () => {
  beforeEach(() => {
    listWebAPIs.mockReset();
    saveWebAPI.mockReset();
    parseWebAPISpec.mockReset();
    listWebAPIs.mockResolvedValue({ web_apis: [] });
    webAPIUsage.mockResolvedValue({ flows: [] });
  });

  const openImporter = async () => {
    render(<AdminWebAPIs />);
    await waitFor(() =>
      expect(screen.getByText("webapi.emptyTitle")).toBeInTheDocument(),
    );
    await userEvent.click(screen.getByText("webapi.add"));
  };

  it("reads a spec and seeds the form with the operations picked", async () => {
    parseWebAPISpec.mockResolvedValue(parsed);
    saveWebAPI.mockResolvedValue({ ...orders });
    await openImporter();

    await userEvent.type(
      screen.getByLabelText("webapi.specURLLabel"),
      "https://api.example.com/openapi.json",
    );
    await userEvent.click(screen.getByText("webapi.specRead"));

    await waitFor(() => expect(screen.getByText("/orders")).toBeInTheDocument());
    expect(screen.getByText(/webapi.specImport.*"count":2/)).toBeInTheDocument();

    await userEvent.click(screen.getByText(/webapi.specImport/));
    await userEvent.click(screen.getByText("webapi.save"));

    await waitFor(() => expect(saveWebAPI).toHaveBeenCalled());
    const [, input] = saveWebAPI.mock.calls[0];
    expect(input.operations).toHaveLength(2);
    expect(input.label).toBe("Order service");
    expect(input.base_url).toBe("https://api.example.com/v1");
    expect(input.spec_url).toBe("https://api.example.com/openapi.json");
  });

  it("reads nothing until asked, and stores nothing when it does", async () => {
    parseWebAPISpec.mockResolvedValue(parsed);
    await openImporter();
    expect(parseWebAPISpec).not.toHaveBeenCalled();

    await userEvent.type(screen.getByLabelText("webapi.specURLLabel"), "https://x.example/s.json");
    await userEvent.click(screen.getByText("webapi.specRead"));
    await waitFor(() => expect(parseWebAPISpec).toHaveBeenCalled());
    expect(saveWebAPI).not.toHaveBeenCalled();
  });

  it("lets an operation be deselected before importing", async () => {
    parseWebAPISpec.mockResolvedValue(parsed);
    saveWebAPI.mockResolvedValue({ ...orders });
    await openImporter();
    await userEvent.type(screen.getByLabelText("webapi.specURLLabel"), "https://x.example/s.json");
    await userEvent.click(screen.getByText("webapi.specRead"));
    await waitFor(() => expect(screen.getByText("/orders")).toBeInTheDocument());

    // Curation is the feature: a spec with hundreds of operations must not put
    // all of them in the palette.
    const boxes = screen.getAllByRole("checkbox");
    await userEvent.click(boxes[0]);
    expect(screen.getByText(/webapi.specImport.*"count":1/)).toBeInTheDocument();

    await userEvent.click(screen.getByText(/webapi.specImport/));
    await userEvent.click(screen.getByText("webapi.save"));
    await waitFor(() => expect(saveWebAPI).toHaveBeenCalled());
    expect(saveWebAPI.mock.calls[0][1].operations).toHaveLength(1);
  });

  // The safety mechanism. An operation can vanish from a spec because someone
  // deleted a handler; the steps it contributed are referenced by saved flows.
  it("will not import a refresh that removes operations until it is confirmed", async () => {
    parseWebAPISpec.mockResolvedValue({
      ...parsed,
      diff: {
        added: 0,
        changed: 0,
        removed: 1,
        unchanged: 2,
        operations: [
          {
            id: "cancel_order",
            change: "removed",
            step_id: "api:order-service:cancel_order",
            method: "DELETE",
            path: "/orders/{order_id}",
          },
        ],
      },
    });
    saveWebAPI.mockResolvedValue({ ...orders });
    listWebAPIs.mockResolvedValue({ web_apis: [orders] });

    render(<AdminWebAPIs />);
    await waitFor(() =>
      expect(screen.getByText("Order service")).toBeInTheDocument(),
    );
    await userEvent.click(screen.getByLabelText("common.edit"));
    await userEvent.type(screen.getByLabelText("webapi.specURLLabel"), "https://x.example/s.json");
    await userEvent.click(screen.getByText("webapi.specRead"));

    await waitFor(() =>
      expect(screen.getByText(/webapi.specRemovalsTitle/)).toBeInTheDocument(),
    );
    // The step id is named, because that is what an admin searches their flows
    // for before agreeing.
    expect(screen.getByText("api:order-service:cancel_order")).toBeInTheDocument();

    const importButton = screen.getByText(/webapi.specImport/).closest("button")!;
    expect(importButton).toBeDisabled();

    await userEvent.click(screen.getByText("webapi.specConfirmRemovals"));
    expect(importButton).not.toBeDisabled();
  });

  // The parser's refusals are written for the admin. They must reach them.
  it("shows why a document could not be read", async () => {
    parseWebAPISpec.mockRejectedValue(new Error("this is a Swagger 2.0 document"));
    await openImporter();
    await userEvent.type(screen.getByLabelText("webapi.specURLLabel"), "https://x.example/s.json");
    await userEvent.click(screen.getByText("webapi.specRead"));
    await waitFor(() => expect(screen.getByRole("alert")).toBeInTheDocument());
  });
});
