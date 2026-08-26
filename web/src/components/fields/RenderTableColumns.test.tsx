// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// The column editor writes render_table's `columns` param, and the shape it
// writes is a contract with the drop (drops/transform/render_table.go): a bare
// name means "head this column with the data's own name", an object means "read
// this field, head it with that text".
//
// A row is a pair — the data column and, optionally, the heading over it — and
// the pair is entered in ONE row. Two earlier shapes of this failed: renaming
// wrote the new name as the key (a correct-looking heading over an empty
// column), and then the heading moved to a separate params field, which put the
// two halves of one decision in two places. These tests are on the param and on
// the row, because that is where both failures were.
import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));
vi.mock("../../auth", () => ({ useAuth: () => ({ token: "tok" }) }));

const getNodeRecord = vi.fn();
const listInputFields = vi.fn();
vi.mock("../../api", () => ({
  api: {
    getNodeRecord: (...a: unknown[]) => getNodeRecord(...a),
    listInputFields: (...a: unknown[]) => listInputFields(...a),
  },
}));

import { RenderTableColumns } from "./RenderTableColumns";

function renderEditor(columns?: unknown, onApply = vi.fn()) {
  render(<RenderTableColumns params={columns === undefined ? {} : { columns }} onApply={onApply} />);
  return onApply;
}

const addBox = () => screen.getByRole("textbox", { name: "renderTableColumns.addPlaceholder" });
const nameBoxes = () => screen.getAllByRole("textbox", { name: "renderTableColumns.customName" });
// The add row's custom-name box is the last one on screen (the edit row, when
// open, renders above it).
const addNameBox = () => nameBoxes()[nameBoxes().length - 1];

// A tap with no movement is what the component treats as "edit this row".
async function tapRow(text: string) {
  const row = screen.getByText(text).closest(".rtc-fg") as HTMLElement;
  await userEvent.pointer([
    { target: row, keys: "[MouseLeft>]" },
    { target: row, keys: "[/MouseLeft]" },
  ]);
}

describe("RenderTableColumns — adding a column", () => {
  it("takes the column and its heading in one row", async () => {
    const onApply = renderEditor(["name"]);
    await userEvent.type(addBox(), "customer_email");
    await userEvent.type(addNameBox(), "Customer{Enter}");
    expect(onApply).toHaveBeenCalledWith({
      columns: ["name", { column: "customer_email", label: "Customer" }],
    });
  });

  it("survives moving between its two boxes", async () => {
    // The bug this pins: committing on a box blur added the row the moment
    // focus left the column box, so the custom name could never be typed.
    const onApply = renderEditor(["name"]);
    await userEvent.type(addBox(), "total");
    await userEvent.click(addNameBox());
    expect(onApply).not.toHaveBeenCalled();
    await userEvent.type(addNameBox(), "Amount{Enter}");
    expect(onApply).toHaveBeenCalledWith({
      columns: ["name", { column: "total", label: "Amount" }],
    });
  });

  it("adds a plain column when no heading is given", async () => {
    const onApply = renderEditor(["name"]);
    await userEvent.type(addBox(), "total{Enter}");
    expect(onApply).toHaveBeenCalledWith({ columns: ["name", "total"] });
  });

  it("commits when focus leaves the row entirely", async () => {
    const onApply = renderEditor(["name"]);
    await userEvent.type(addBox(), "total");
    await userEvent.type(addNameBox(), "Amount");
    await userEvent.click(screen.getByText("name"));
    expect(onApply).toHaveBeenCalledWith({
      columns: ["name", { column: "total", label: "Amount" }],
    });
  });

  it("ignores a heading with no column", async () => {
    const onApply = renderEditor(["name"]);
    await userEvent.type(addNameBox(), "Customer{Enter}");
    expect(onApply).not.toHaveBeenCalled();
  });
});

describe("RenderTableColumns — editing a row", () => {
  it("shows the column and its heading as a pair", () => {
    renderEditor([{ column: "customer_email", label: "Customer" }]);
    // Both halves on screen: the field it reads, and the heading it shows.
    expect(screen.getByText("customer_email")).toBeInTheDocument();
    expect(screen.getByText("Customer")).toBeInTheDocument();
  });

  it("shows only the column when there is no custom name", () => {
    const { container } = render(
      <RenderTableColumns params={{ columns: ["name"] }} onApply={() => {}} />,
    );
    expect(screen.getByText("name")).toBeInTheDocument();
    expect(container.querySelector(".rtc-as")).toBeNull();
  });

  it("sets a heading without touching the data column", async () => {
    const onApply = renderEditor(["customer_email", "created_at"]);
    await tapRow("customer_email");
    await userEvent.type(nameBoxes()[0], "Customer{Enter}");
    expect(onApply).toHaveBeenCalledWith({
      columns: [{ column: "customer_email", label: "Customer" }, "created_at"],
    });
  });

  it("opens with the column filled in and the heading blank when unnamed", async () => {
    renderEditor(["customer_email"]);
    await tapRow("customer_email");
    expect(screen.getByRole("textbox", { name: "renderTableColumns.columnField" })).toHaveValue(
      "customer_email",
    );
    // Blank, not pre-filled with the value it is meant to replace.
    expect(nameBoxes()[0]).toHaveValue("");
  });

  it("re-points a row at a different column", async () => {
    const onApply = renderEditor([{ column: "customer_email", label: "Customer" }]);
    await tapRow("customer_email");
    const col = screen.getByRole("textbox", { name: "renderTableColumns.columnField" });
    await userEvent.clear(col);
    await userEvent.type(col, "billing_email{Enter}");
    expect(onApply).toHaveBeenCalledWith({
      columns: [{ column: "billing_email", label: "Customer" }],
    });
  });

  it("clearing the heading goes back to the data's own name", async () => {
    const onApply = renderEditor([{ column: "name", label: "Who" }]);
    await tapRow("Who");
    await userEvent.clear(nameBoxes()[0]);
    await userEvent.type(nameBoxes()[0], "{Enter}");
    expect(onApply).toHaveBeenCalledWith({ columns: ["name"] });
  });

  it("refuses to point two rows at the same column", async () => {
    const onApply = renderEditor(["a", "b"]);
    await tapRow("a");
    const col = screen.getByRole("textbox", { name: "renderTableColumns.columnField" });
    await userEvent.clear(col);
    await userEvent.type(col, "b{Enter}");
    // The same column twice would render twice, with one heading each.
    expect(onApply).not.toHaveBeenCalled();
  });

  it("keeps the row when the column box is emptied", async () => {
    // Removing a row is the swipe; an empty box is an unfinished edit.
    const onApply = renderEditor(["name"]);
    await tapRow("name");
    const col = screen.getByRole("textbox", { name: "renderTableColumns.columnField" });
    await userEvent.clear(col);
    await userEvent.type(col, "{Enter}");
    expect(onApply).not.toHaveBeenCalled();
    expect(screen.getByText("name")).toBeInTheDocument();
  });

  it("abandons the edit on Escape", async () => {
    const onApply = renderEditor(["name"]);
    await tapRow("name");
    await userEvent.type(nameBoxes()[0], "Who{Escape}");
    expect(onApply).not.toHaveBeenCalled();
  });
});

describe("RenderTableColumns — the saved param", () => {
  it("reads a param that mixes both shapes", () => {
    renderEditor(["name", { column: "customer_email", label: "Customer" }]);
    expect(screen.getByText("name")).toBeInTheDocument();
    expect(screen.getByText("Customer")).toBeInTheDocument();
  });

  it("leaves an unnamed column as a plain string", async () => {
    // A flow that never sets a heading keeps the exact param it had.
    const onApply = renderEditor(["b", "a"]);
    await tapRow("a");
    await userEvent.type(nameBoxes()[0], "Alpha{Enter}");
    expect(onApply).toHaveBeenCalledWith({ columns: ["b", { column: "a", label: "Alpha" }] });
  });

  it("keeps pointing at the original field when renamed twice", async () => {
    // The first regression in miniature: if a rename moved the key, the second
    // rename would be editing a field that no longer exists.
    const onApply = renderEditor([{ column: "created_at", label: "Ordered" }]);
    await tapRow("Ordered");
    await userEvent.clear(nameBoxes()[0]);
    await userEvent.type(nameBoxes()[0], "Order date{Enter}");
    expect(onApply).toHaveBeenCalledWith({
      columns: [{ column: "created_at", label: "Order date" }],
    });
  });
});

// Where the list comes from when the user hasn't curated one. This is the half
// that was broken: the editor asked for its OWN resolved input, which a node
// record never carries, so for every producer that can't declare its fields —
// a JSON step, a query, an HTTP call — the list stayed empty for ever and the
// empty state's "run it once" advice could not come true.
describe("RenderTableColumns — discovering the columns", () => {
  const rowsSource = { nodeId: "json_1", port: "out" };

  it("lists the columns the producer just emitted", async () => {
    render(
      <RenderTableColumns
        params={{}}
        onApply={() => {}}
        rowsSource={rowsSource}
        upstreamRows={[{ customer_email: "a@b.c", created_at: "2026-08-01" }]}
      />,
    );
    await waitFor(() => expect(screen.getByText("customer_email")).toBeInTheDocument());
    expect(screen.getByText("created_at")).toBeInTheDocument();
    // Live rows are already in hand; no need to go back to the server for them.
    expect(getNodeRecord).not.toHaveBeenCalled();
  });

  it("reads them back off the stored run when the stream is gone", async () => {
    // What a reload looks like: no live rows, but a run id and an edge.
    getNodeRecord.mockResolvedValue({
      Result: { output: { out: { data: [{ name: "Ada" }, { name: "Bo", note: "late" }] } } },
    });
    render(
      <RenderTableColumns
        params={{}}
        onApply={() => {}}
        currentRunID="run_1"
        rowsSource={rowsSource}
      />,
    );
    await waitFor(() => expect(screen.getByText("name")).toBeInTheDocument());
    // The producer's node is the one asked, not this step.
    expect(getNodeRecord).toHaveBeenCalledWith("tok", "run_1", "json_1");
    // And a column that only shows up in the second row still counts.
    expect(screen.getByText("note")).toBeInTheDocument();
  });

  it("says so, rather than showing a broken list, when nothing is known", async () => {
    render(<RenderTableColumns params={{}} onApply={() => {}} rowsSource={rowsSource} />);
    await waitFor(() =>
      expect(screen.getByText("renderTableColumns.empty")).toBeInTheDocument(),
    );
  });

  it("leaves a curated list alone", async () => {
    // The saved set is authoritative: a column the user hid must not come back
    // just because the producer still emits it.
    render(
      <RenderTableColumns
        params={{ columns: ["created_at"] }}
        onApply={() => {}}
        rowsSource={rowsSource}
        upstreamRows={[{ customer_email: "a@b.c", created_at: "2026-08-01" }]}
      />,
    );
    await waitFor(() => expect(screen.getByText("created_at")).toBeInTheDocument());
    const shown = document.querySelectorAll(".rtc-list > .rtc-item:not(.rtc-add-row):not(.rtc-hidden-row)");
    expect(shown).toHaveLength(1);
    // The dropped one is offered back under "hidden", not silently restored.
    expect(document.querySelectorAll(".rtc-hidden-row")).toHaveLength(1);
  });
});

// Leaving the panel without leaving the field. Clicking the canvas is how you
// leave a panel, and React Flow preventDefaults the pane's mousedown so it can
// start a drag — focus stays in the box, no blur fires, and the click then
// deselects the step and unmounts this editor. React fires no blur on unmount
// either, so a name typed and left that way was discarded.
describe("RenderTableColumns — the panel going away mid-edit", () => {
  it("saves a heading typed into an open row", async () => {
    const onApply = vi.fn();
    const { unmount } = render(
      <RenderTableColumns params={{ columns: ["total"] }} onApply={onApply} />,
    );
    await tapRow("total");
    // Typed, never confirmed: no Enter, no blur.
    await userEvent.type(nameBoxes()[0], "Belopp");
    expect(onApply).not.toHaveBeenCalled();

    unmount();
    expect(onApply).toHaveBeenCalledWith({
      columns: [{ column: "total", label: "Belopp" }],
    });
  });

  it("saves a column re-pointed in an open row", async () => {
    const onApply = vi.fn();
    const { unmount } = render(
      <RenderTableColumns params={{ columns: ["total"] }} onApply={onApply} />,
    );
    await tapRow("total");
    const col = screen.getByRole("textbox", { name: "renderTableColumns.columnField" });
    await userEvent.clear(col);
    await userEvent.type(col, "amount");
    unmount();
    expect(onApply).toHaveBeenCalledWith({ columns: ["amount"] });
  });

  it("saves a column half-added in the add row", async () => {
    const onApply = vi.fn();
    const { unmount } = render(
      <RenderTableColumns params={{ columns: ["name"] }} onApply={onApply} />,
    );
    await userEvent.type(addBox(), "total");
    await userEvent.type(addNameBox(), "Belopp");
    unmount();
    expect(onApply).toHaveBeenCalledWith({
      columns: ["name", { column: "total", label: "Belopp" }],
    });
  });

  it("writes nothing when there was nothing being typed", () => {
    const onApply = vi.fn();
    const { unmount } = render(
      <RenderTableColumns params={{ columns: ["name"] }} onApply={onApply} />,
    );
    unmount();
    // Opening a panel and closing it must not touch the flow.
    expect(onApply).not.toHaveBeenCalled();
  });

  it("writes nothing when the add row holds only a heading", async () => {
    const onApply = vi.fn();
    const { unmount } = render(
      <RenderTableColumns params={{ columns: ["name"] }} onApply={onApply} />,
    );
    await userEvent.type(addNameBox(), "Belopp");
    unmount();
    // A heading with no column names nothing.
    expect(onApply).not.toHaveBeenCalled();
  });
});
