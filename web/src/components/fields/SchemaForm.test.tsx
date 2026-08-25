// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// t() returns the key, with {{interpolations}} filled in — enough for a test to
// assert on the part of a label that comes from the data ("old-laptop —
// offline") without pulling the real catalogues in.
const fakeT = (k: string, vars?: Record<string, unknown>) =>
  vars ? k + " " + Object.values(vars).join(" ") : k;
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: fakeT }),
}));
// nodeCardShared imports the real i18n singleton (which inits a browser
// language detector that doesn't run in jsdom); stub it to a plain t().
vi.mock("../../i18n", () => ({ default: { t: (k: string) => k } }));
vi.mock("../../auth", () => ({
  useAuth: () => ({ token: null, hasPerm: () => false }),
}));

import { SchemaForm } from "./SchemaForm";
import { api } from "../../api";
import type { JSONSchema } from "../../types";

const stringSchema: JSONSchema = {
  type: "object",
  properties: { note: { type: "string", title: "Note" } },
} as JSONSchema;

describe("SchemaForm (FormContext)", () => {
  it("renders a basic field and reports edits — no per-field context props needed", async () => {
    const onChange = vi.fn();
    // No workspace/accountPicker/references/tokenLabels passed: they flow via
    // FormContext (undefined here), and a plain string field must still work.
    render(<SchemaForm schema={stringSchema} value={{}} onChange={onChange} />);

    const input = screen.getByRole("textbox");
    await userEvent.type(input, "hi");
    expect(onChange).toHaveBeenCalled();
    // The last call reflects the typed value merged into the params object.
    const last = onChange.mock.calls.at(-1)?.[0];
    expect(last).toMatchObject({ note: expect.any(String) });
  });

  it("renders nothing actionable for a non-object schema (fallback hint)", () => {
    render(
      <SchemaForm
        schema={{ type: "string" } as JSONSchema}
        value={{}}
        onChange={() => {}}
      />,
    );
    expect(screen.getByText("schemaForm.fallbackHint")).toBeInTheDocument();
  });
});

// ---- the Run on your machine step's fields ----------------------------

// A token per test: the machine list is cached per token (one inspector fills
// two fields from it), so sharing one would serve the previous test's answer.
const refs = (token: string) => ({
  token,
  tenant: "acme",
  workspace: "main",
  flowId: "acme/main/f1",
  nodeId: "n1",
});

const runnerSchema: JSONSchema = {
  type: "object",
  properties: {
    runner: { type: "string", title: "Machine", format: "runner" },
    label: { type: "string", title: "Or any machine labelled", format: "runner-label" },
    shell: { type: "string", title: "Run it with", default: "default", enum: ["default", "python"] },
    script: { type: "string", title: "Script", format: "script" },
  },
} as JSONSchema;

describe("SchemaForm — the runner step's fields", () => {
  it("offers the org's machines as a dropdown instead of a name to retype", async () => {
    const token = "list";
    // The name exists in exactly one place the flow author cannot see from the
    // editor, and a typo used to surface only when a run failed.
    vi.spyOn(api, "listRunnerTargets").mockResolvedValue({
      runners: [
        { name: "invoices-box", labels: ["linux", "build"], online: true },
        { name: "old-laptop", labels: ["linux"], online: false },
      ],
    });
    render(
      <SchemaForm schema={runnerSchema} value={{}} onChange={() => {}} references={refs(token)} />,
    );

    const machine = await screen.findByRole("option", { name: "invoices-box" });
    expect(machine).toBeInTheDocument();
    // An offline machine is still offered — a step pointed at one saves, waits
    // and then fails, so it has to be visible while choosing, not after a run.
    expect(await screen.findByRole("option", { name: /old-laptop/ })).toBeInTheDocument();

    // Labels are de-duplicated across machines: they name a pool, so "linux" on
    // two machines is one choice.
    const labels = await screen.findAllByRole("option", { name: /^schemaForm.runner.labelOption/ });
    expect(labels).toHaveLength(2);
  });

  it("keeps a target that is no longer registered rather than clearing it", async () => {
    const token = "gone";
    // Silently blanking a step's target would be a worse failure than showing
    // one that needs attention.
    vi.spyOn(api, "listRunnerTargets").mockResolvedValue({
      runners: [{ name: "invoices-box", online: true }],
    });
    render(
      <SchemaForm
        schema={runnerSchema}
        value={{ runner: "decommissioned" }}
        onChange={() => {}}
        references={refs(token)}
      />,
    );
    expect(await screen.findByRole("option", { name: /decommissioned/ }))
      .toHaveValue("decommissioned");
  });

  it("falls back to a text box when there are no machines to list", async () => {
    const token = "empty";
    // A deployment without runners answers 501, and an org may have registered
    // none yet. Either way the field must stay usable — a name typed now, or a
    // ${…} reference — rather than becoming an empty dropdown.
    vi.spyOn(api, "listRunnerTargets").mockRejectedValue(new Error("501"));
    render(
      <SchemaForm schema={runnerSchema} value={{}} onChange={() => {}} references={refs(token)} />,
    );
    expect(await screen.findByText(/schemaForm.runner.none/)).toBeInTheDocument();
  });

  it("gives the script a highlighted code box, coloured for the chosen language", async () => {
    const token = "script";
    vi.spyOn(api, "listRunnerTargets").mockResolvedValue({ runners: [] });
    const { container } = render(
      <SchemaForm
        schema={runnerSchema}
        value={{ shell: "python", script: "import sys  # go" }}
        onChange={() => {}}
        references={refs(token)}
      />,
    );
    // Not an <input>: a script is many lines by nature and everything past the
    // right edge of a one-line box was invisible.
    const box = container.querySelector(".dz-code-editor textarea");
    expect(box).toHaveValue("import sys  # go");
    // Coloured as Python, which only the sibling `shell` param can say.
    expect(container.querySelector(".dz-s-keyword")).toHaveTextContent("import");
    expect(container.querySelector(".dz-s-comment")).toHaveTextContent("# go");
  });
});
