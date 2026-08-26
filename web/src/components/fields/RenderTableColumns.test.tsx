// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// The column editor writes render_table's `columns` param, and the shape it
// writes is a contract with the drop (drops/transform/render_table.go): a bare
// name means "head this column with the data's own name", an object means
// "read this field, head it with that text".
//
// Renaming used to write the new name as the KEY, which is what the drop reads
// its cells by — so a renamed column shipped a correct-looking header over an
// entirely blank column, in every email the flow sent. These tests are on the
// param, not the pixels, because the param is what was wrong.
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));
vi.mock("../../auth", () => ({ useAuth: () => ({ token: null }) }));

import { RenderTableColumns } from "./RenderTableColumns";

function renderEditor(columns: unknown, onApply = vi.fn()) {
  render(<RenderTableColumns params={{ columns }} onApply={onApply} />);
  return onApply;
}

// A tap with no movement is what the component treats as "rename this one".
async function tapRow(name: string) {
  const row = screen.getByText(name).closest(".rtc-fg") as HTMLElement;
  await userEvent.pointer([{ target: row, keys: "[MouseLeft>]" }, { target: row, keys: "[/MouseLeft]" }]);
}

describe("RenderTableColumns", () => {
  it("renames the header and keeps the data column", async () => {
    const onApply = renderEditor(["customer_email", "created_at"]);
    await tapRow("customer_email");
    const box = screen.getByRole("textbox", { name: "renderTableColumns.rename" });
    await userEvent.clear(box);
    await userEvent.type(box, "Customer{Enter}");

    expect(onApply).toHaveBeenCalledWith({
      columns: [{ column: "customer_email", label: "Customer" }, "created_at"],
    });
  });

  it("shows the header text with the field it reads underneath", () => {
    renderEditor([{ column: "customer_email", label: "Customer" }]);
    expect(screen.getByText("Customer")).toBeInTheDocument();
    // Both are on screen: the header text can't be mistaken for the data's own
    // name, and a rename is visibly a rename rather than a re-point.
    expect(screen.getByText("customer_email")).toBeInTheDocument();
  });

  it("leaves an unrenamed column as a plain name", async () => {
    // Reordering a set nobody has renamed must not rewrite it into objects —
    // the saved param stays what it was.
    const onApply = renderEditor(["b", "a"]);
    await tapRow("a");
    const box = screen.getByRole("textbox", { name: "renderTableColumns.rename" });
    await userEvent.clear(box);
    await userEvent.type(box, "Alpha{Enter}");
    expect(onApply).toHaveBeenCalledWith({ columns: ["b", { column: "a", label: "Alpha" }] });
  });

  it("clearing the header text goes back to the data's own name", async () => {
    const onApply = renderEditor([{ column: "name", label: "Who" }]);
    await tapRow("Who");
    const box = screen.getByRole("textbox", { name: "renderTableColumns.rename" });
    await userEvent.clear(box);
    await userEvent.type(box, "{Enter}");
    // Not a column with a blank header, and not a column keyed "".
    expect(onApply).toHaveBeenCalledWith({ columns: ["name"] });
  });

  it("renaming twice keeps pointing at the original field", async () => {
    // The regression in miniature: rename, then rename again. If the first
    // rename moved the key, the second one renames a field that doesn't exist.
    const onApply = renderEditor([{ column: "created_at", label: "Ordered" }]);
    await tapRow("Ordered");
    const box = screen.getByRole("textbox", { name: "renderTableColumns.rename" });
    await userEvent.clear(box);
    await userEvent.type(box, "Order date{Enter}");
    expect(onApply).toHaveBeenCalledWith({
      columns: [{ column: "created_at", label: "Order date" }],
    });
  });

  it("reads a saved param that mixes both shapes", () => {
    renderEditor(["name", { column: "customer_email", label: "Customer" }]);
    expect(screen.getByText("name")).toBeInTheDocument();
    expect(screen.getByText("Customer")).toBeInTheDocument();
  });

  it("adds a column by name as a plain name", async () => {
    const onApply = renderEditor(["name"]);
    const add = screen.getByRole("textbox", { name: "renderTableColumns.addPlaceholder" });
    await userEvent.type(add, "total{Enter}");
    expect(onApply).toHaveBeenCalledWith({ columns: ["name", "total"] });
  });
});
