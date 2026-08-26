// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("react-i18next", () => {
  const t = (k: string, o?: Record<string, unknown>) =>
    o && typeof o === "object" ? `${k}:${Object.values(o).join(",")}` : k;
  const value = { t, i18n: { language: "en" } };
  return { useTranslation: () => value };
});
vi.mock("../auth", () => {
  const auth = { token: "tok", me: { tenant: "t", workspace: "ws" } };
  return { useAuth: () => auth };
});

const listBoards = vi.fn();
const getBoard = vi.fn();
vi.mock("../api", () => ({
  APIError: class extends Error {},
  api: {
    listBoards: (...a: unknown[]) => listBoards(...a),
    getBoard: (...a: unknown[]) => getBoard(...a),
  },
}));

import { Results } from "./Results";

// The columns of a collection are the user's data, not our vocabulary. Every
// other .run-table in the app heads its columns with a label we wrote, and the
// shared style upper-cases them ("STATUS"); applied here it reported
// `orderTotal` as ORDERTOTAL — a name that appears nowhere in the data, in a
// step, or in the CSV the same page downloads.
describe("Collections table headers", () => {
  const setup = () => {
    listBoards.mockResolvedValue({ boards: [{ name: "orders", rows: 1 }] });
    getBoard.mockResolvedValue({
      name: "orders",
      columns: ["Ordered", "customer_email", "orderTotal"],
      rows: [
        {
          _dz_rowid: 1,
          Ordered: "2026-08-01",
          customer_email: "ada@example.com",
          orderTotal: "12",
        },
      ],
      total: 1,
      truncated: false,
    });
    return render(<Results />);
  };

  it("prints the collection's column names exactly as stored", async () => {
    const { container } = setup();
    await waitFor(() => expect(screen.getByText("customer_email")).toBeInTheDocument());
    const headers = [...container.querySelectorAll("thead th")].map((th) => th.textContent);
    // Including case: a header is a name someone has to match against their
    // data, so "Ordered" is not "ordered" and `orderTotal` keeps its hump.
    expect(headers).toEqual(["Ordered", "customer_email", "orderTotal", ""]);
  });

  it("marks the table as data-headed so the label casing doesn't apply", async () => {
    // The uppercasing lives in CSS, which jsdom doesn't apply — the class is
    // the only thing a test can hold onto, and losing it is how the bug
    // returns.
    const { container } = setup();
    await waitFor(() => expect(screen.getByText("customer_email")).toBeInTheDocument());
    expect(container.querySelector("table.run-table")).toHaveClass("data-headers");
  });
});

// Sorting the table. The requirement that carries the weight is the CSV one: a
// download that ignores the ordering on screen is a spreadsheet nobody can
// check against the page they asked for it from.
describe("Collections sorting", () => {
  const rows = [
    { _dz_rowid: 1, name: "Carol", spend: "100" },
    { _dz_rowid: 2, name: "Alice", spend: "20" },
    { _dz_rowid: 3, name: "Bob", spend: "9" },
  ];
  const orders = {
    name: "orders",
    columns: ["name", "spend"],
    rows,
    total: 3,
    truncated: false,
  };

  const setup = (boards = [{ name: "orders", rows: 3 }]) => {
    listBoards.mockResolvedValue({ boards });
    getBoard.mockResolvedValue(orders);
    return render(<Results />);
  };

  const bodyColumn = (container: HTMLElement, idx: number) =>
    [...container.querySelectorAll("tbody tr")].map(
      (tr) => tr.querySelectorAll("td")[idx]?.textContent,
    );

  // The header's own text is its accessible name — the column name is what a
  // reader is looking for, and what a screen reader should announce.
  const header = (col: string) => screen.getByRole("button", { name: col });
  const ready = () => waitFor(() => expect(header("name")).toBeTruthy());

  it("starts in the order the rows were saved", async () => {
    const { container } = setup();
    await ready();
    expect(bodyColumn(container, 0)).toEqual(["Carol", "Alice", "Bob"]);
  });

  it("cycles a column ascending, descending, then back to saved order", async () => {
    const { container } = setup();
    await ready();
    await userEvent.click(header("name"));
    expect(bodyColumn(container, 0)).toEqual(["Alice", "Bob", "Carol"]);
    await userEvent.click(header("name"));
    expect(bodyColumn(container, 0)).toEqual(["Carol", "Bob", "Alice"]);
    await userEvent.click(header("name"));
    expect(bodyColumn(container, 0)).toEqual(["Carol", "Alice", "Bob"]);
  });

  it("sorts a column of numbers by value, not as text", async () => {
    // The store is all TEXT, so "9" against "100" is the case a naive string
    // compare gets wrong.
    const { container } = setup();
    await ready();
    await userEvent.click(header("spend"));
    expect(bodyColumn(container, 1)).toEqual(["9", "20", "100"]);
  });

  it("says which column is sorted, and which way", async () => {
    const { container } = setup();
    await ready();
    await userEvent.click(header("name"));
    const ths = () => [...container.querySelectorAll("thead th")];
    expect(ths()[0].getAttribute("aria-sort")).toBe("ascending");
    expect(ths()[1].getAttribute("aria-sort")).toBe("none");
    await userEvent.click(header("name"));
    expect(ths()[0].getAttribute("aria-sort")).toBe("descending");
  });

  it("exports the CSV in the order shown on screen", async () => {
    // The download builds a Blob and hands it to URL.createObjectURL. jsdom
    // implements neither the object-URL methods nor Blob.text(), so the CSV is
    // captured from the Blob constructor's own argument.
    const written: string[] = [];
    const g = globalThis as unknown as Record<string, unknown>;
    const origBlob = g.Blob;
    const origCreate = (URL as unknown as Record<string, unknown>).createObjectURL;
    const origRevoke = (URL as unknown as Record<string, unknown>).revokeObjectURL;
    g.Blob = class {
      constructor(parts: string[]) {
        written.push(parts.join(""));
      }
    };
    // "#": a blob: URL makes jsdom log a page-navigation stack trace when the
    // download link is clicked. The URL is never fetched by the test.
    (URL as unknown as Record<string, unknown>).createObjectURL = () => "#";
    (URL as unknown as Record<string, unknown>).revokeObjectURL = () => {};

    setup();
    await ready();
    await userEvent.click(header("name"));
    await userEvent.click(screen.getByRole("button", { name: "results.downloadCsv" }));

    expect(written).toHaveLength(1);
    const names = written[0]
      .split("\n")
      .slice(1)
      .map((line) => line.split(",")[0]);
    expect(names).toEqual(['"Alice"', '"Bob"', '"Carol"']);

    g.Blob = origBlob;
    (URL as unknown as Record<string, unknown>).createObjectURL = origCreate;
    (URL as unknown as Record<string, unknown>).revokeObjectURL = origRevoke;
  });

  it("drops the sort when another collection is opened", async () => {
    // The next collection has its own columns, so the sorted one usually isn't
    // among them.
    const { container } = setup([
      { name: "orders", rows: 3 },
      { name: "leads", rows: 3 },
    ]);
    await ready();
    await userEvent.click(header("name"));
    expect(container.querySelectorAll("thead th")[0].getAttribute("aria-sort")).toBe("ascending");

    getBoard.mockResolvedValue({ ...orders, name: "leads" });
    await userEvent.click(screen.getByRole("button", { name: /leads/ }));
    await waitFor(() =>
      expect(container.querySelectorAll("thead th")[0].getAttribute("aria-sort")).toBe("none"),
    );
  });
});
