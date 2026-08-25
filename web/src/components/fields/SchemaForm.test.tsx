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
    tags: {
      type: "array",
      title: "Where to run it",
      format: "runner-tags",
      items: { type: "string" },
    },
    shell: { type: "string", title: "Run it with", default: "default", enum: ["default", "python"] },
    script: { type: "string", title: "Script", format: "script" },
  },
} as JSONSchema;

describe("SchemaForm — the runner step's fields", () => {
  // One field replaced two (a machine name and a label, mutually exclusive),
  // which is possible because every machine now carries its own name as a tag.
  it("offers every tag the org's machines carry, names included", async () => {
    const token = "list";
    vi.spyOn(api, "listRunnerTargets").mockResolvedValue({
      runners: [
        { name: "invoices-box", tags: ["build", "invoices-box", "linux"], online: true },
        { name: "old-laptop", tags: ["linux", "old-laptop"], online: false },
      ],
    });
    render(
      <SchemaForm schema={runnerSchema} value={{}} onChange={() => {}} references={refs(token)} />,
    );

    // The count is what says whether a tag is a pool or one machine.
    expect(await screen.findByText(/schemaForm.runner.tagOption.*linux.*2/)).toBeInTheDocument();
    expect(await screen.findByText(/schemaForm.runner.tagOption.*invoices-box.*1/)).toBeInTheDocument();
  });

  // Work goes to whichever tagged machine is polling when the step fires, so
  // the tags backed by a machine that is actually on are the ones worth
  // reaching for — they lead.
  it("puts tags with a machine switched on first", async () => {
    const token = "order";
    vi.spyOn(api, "listRunnerTargets").mockResolvedValue({
      runners: [
        { name: "off-box", tags: ["alpha-pool", "off-box"], online: false },
        { name: "on-box", tags: ["zeta-pool", "on-box"], online: true },
      ],
    });
    render(
      <SchemaForm schema={runnerSchema} value={{}} onChange={() => {}} references={refs(token)} />,
    );
    await screen.findByText(/zeta-pool/);
    const labels = [...document.querySelectorAll(".sf-multiselect-opt")].map((e) => e.textContent ?? "");
    const at = (tag: string) => labels.findIndex((l) => l.includes(tag));
    // zeta-pool has a machine that is on; alpha-pool does not. So it leads,
    // even though alphabetically it would come last — which is the whole point.
    expect(at("zeta-pool")).toBeLessThan(at("alpha-pool"));
    // Within each group the order stays alphabetical, so the list does not
    // reshuffle as machines come and go.
    expect(at("on-box")).toBeLessThan(at("zeta-pool"));
    expect(at("alpha-pool")).toBeLessThan(at("off-box"));
  });

  // Machines carry the tags but not one is on: the step will wait and then
  // fail. A different problem from a set that matches nothing, and one the
  // "— 0 online" count let people read straight past.
  it("warns when the matching machines are all switched off", async () => {
    const token = "asleep";
    vi.spyOn(api, "listRunnerTargets").mockResolvedValue({
      runners: [
        { name: "render-01", tags: ["gpu", "render-01"], online: false },
        { name: "render-02", tags: ["gpu", "render-02"], online: false },
      ],
    });
    const { container } = render(
      <SchemaForm
        schema={runnerSchema}
        value={{ tags: ["gpu"] }}
        onChange={() => {}}
        references={refs(token)}
      />,
    );
    // Names them, because which machine to go and switch on is the answer.
    const warn = await screen.findByText(/schemaForm.runner.matchesNoneOnline.*render-01, render-02/);
    expect(warn).toBeInTheDocument();
    expect(container.querySelector(".sf-warn")).not.toBeNull();
  });

  it("ticks tags into the params as a list", async () => {
    const token = "tick";
    const onChange = vi.fn();
    vi.spyOn(api, "listRunnerTargets").mockResolvedValue({
      runners: [{ name: "box", tags: ["box", "linux"], online: true }],
    });
    render(
      <SchemaForm schema={runnerSchema} value={{}} onChange={onChange} references={refs(token)} />,
    );
    const linux = await screen.findByText(/schemaForm.runner.tagOption.*linux/);
    await userEvent.click(linux);
    expect(onChange.mock.calls.at(-1)?.[0]).toMatchObject({ tags: ["linux"] });
  });

  // Every tag must match, so a set can narrow to nothing — and that failure is
  // otherwise invisible until a run. The field says how many machines qualify.
  it("says how many machines carry all the chosen tags", async () => {
    const token = "count";
    vi.spyOn(api, "listRunnerTargets").mockResolvedValue({
      runners: [
        { name: "a", tags: ["a", "linux"], online: true },
        { name: "b", tags: ["b", "linux", "gpu"], online: false },
      ],
    });
    render(
      <SchemaForm
        schema={runnerSchema}
        value={{ tags: ["linux"] }}
        onChange={() => {}}
        references={refs(token)}
      />,
    );
    // Two carry linux, one of them online.
    expect(await screen.findByText(/schemaForm.runner.matches.*2.*1/)).toBeInTheDocument();
  });

  it("warns when no machine carries the whole set", async () => {
    const token = "none";
    vi.spyOn(api, "listRunnerTargets").mockResolvedValue({
      runners: [{ name: "a", tags: ["a", "linux"], online: true }],
    });
    render(
      <SchemaForm
        schema={runnerSchema}
        value={{ tags: ["linux", "gpu"] }}
        onChange={() => {}}
        references={refs(token)}
      />,
    );
    expect(await screen.findByText("schemaForm.runner.matchesNone")).toBeInTheDocument();
  });

  it("stays usable when the machine list cannot be fetched", async () => {
    // A deployment without runners answers 501, and an org may have registered
    // none yet. The tags still have to be typeable by hand.
    const token = "failed";
    vi.spyOn(api, "listRunnerTargets").mockRejectedValue(new Error("501"));
    render(
      <SchemaForm schema={runnerSchema} value={{}} onChange={() => {}} references={refs(token)} />,
    );
    expect(await screen.findByText("schemaForm.runner.noneKnown")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("schemaForm.multiSelectCustom")).toBeInTheDocument();
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
