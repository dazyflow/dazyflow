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

  // x_visible_when: a field that only applies once a sibling says so. The Date
  // & time step's Custom format is the case — beside a Format dropdown, a
  // permanent second format box reads as two ways of saying the same thing.
  const conditional: JSONSchema = {
    type: "object",
    properties: {
      format: {
        type: "string",
        title: "Format",
        default: "iso",
        enum: ["iso", "custom"],
        enumNames: ["ISO-8601", "Custom…"],
      },
      custom_format: {
        type: "string",
        title: "Custom format",
        x_visible_when: { format: "custom" },
      },
    },
  } as JSONSchema;

  it("hides a conditional field until its sibling selects it", () => {
    // Fresh node: format is unset, so its default "iso" is in force.
    const { rerender } = render(
      <SchemaForm schema={conditional} value={{}} onChange={() => {}} />,
    );
    expect(screen.queryByText("Custom format")).not.toBeInTheDocument();

    rerender(
      <SchemaForm schema={conditional} value={{ format: "custom" }} onChange={() => {}} />,
    );
    expect(screen.getByText("Custom format")).toBeInTheDocument();
  });

  it("keeps a hidden conditional field's stored value", () => {
    // Switching away must not clear what was typed — flip back and it's there.
    const onChange = vi.fn();
    render(
      <SchemaForm
        schema={conditional}
        value={{ format: "iso", custom_format: "DD/MM/YYYY" }}
        onChange={onChange}
      />,
    );
    expect(screen.queryByText("Custom format")).not.toBeInTheDocument();
    // Hiding is a render decision only: nothing was written back.
    expect(onChange).not.toHaveBeenCalled();
  });

  // A stored value the enum no longer lists — a retired option, or a param
  // that accepts more than the dropdown offers (any IANA timezone). Without
  // its own option the select displays the FIRST option while the param holds
  // something else, so the form lies and one idle click rewrites it.
  it("keeps an option for a stored value the enum no longer lists", () => {
    const schema: JSONSchema = {
      type: "object",
      properties: {
        tz: {
          type: "string",
          title: "Timezone",
          default: "UTC",
          enum: ["UTC", "Europe/Stockholm"],
          enumNames: ["UTC", "Europe/Stockholm"],
        },
      },
    } as JSONSchema;
    render(
      <SchemaForm schema={schema} value={{ tz: "Africa/Nairobi" }} onChange={() => {}} />,
    );
    const select = screen.getByRole("combobox") as HTMLSelectElement;
    expect(select.value).toBe("Africa/Nairobi");
    expect(screen.getByRole("option", { name: "Africa/Nairobi" })).toBeInTheDocument();
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

// The environment-variable editor, and every other string-keyed map param.
//
// The row used to be derived from the object, which meant it existed only while
// its key did: clearing the name to retype it dropped the entry and the row
// disappeared from under the cursor. Typing a name was only possible by editing
// around the old text and never emptying the box.
describe("SchemaForm — a name/value map", () => {
  const dictSchema: JSONSchema = {
    type: "object",
    properties: {
      env: {
        type: "object",
        title: "Environment variables",
        additionalProperties: { type: "string" },
      },
    },
  } as JSONSchema;

  // queryAll, not getAll: the zero case is a real assertion here (the row is
  // gone after a confirmed removal), and getAll throws rather than returning [].
  const keyBoxes = () => screen.queryAllByPlaceholderText("schemaForm.keyPlaceholder");

  it("keeps the row while its name is being retyped", async () => {
    const onChange = vi.fn();
    render(
      <SchemaForm schema={dictSchema} value={{ env: { OLD: "v" } }} onChange={onChange} />,
    );
    const key = keyBoxes()[0];
    await userEvent.clear(key);

    // The row is still there with its value, which is the whole bug: an empty
    // name means "being typed", not "deleted".
    expect(keyBoxes()).toHaveLength(1);
    expect(screen.getByDisplayValue("v")).toBeInTheDocument();
    // And the params no longer carry a half-named entry.
    expect(onChange.mock.calls.at(-1)?.[0]).toMatchObject({ env: {} });

    await userEvent.type(key, "NEW");
    expect(onChange.mock.calls.at(-1)?.[0]).toMatchObject({ env: { NEW: "v" } });
  });

  it("adds a row with an empty name rather than a placeholder to delete", async () => {
    const onChange = vi.fn();
    render(<SchemaForm schema={dictSchema} value={{ env: {} }} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: /schemaForm.add/ }));
    expect(keyBoxes()).toHaveLength(1);
    expect(keyBoxes()[0]).toHaveValue("");
  });

  it("removes a row when its remove button is used, not when its name is cleared", async () => {
    const onChange = vi.fn();
    render(
      <SchemaForm
        schema={dictSchema}
        value={{ env: { A: "1", B: "2" } }}
        onChange={onChange}
      />,
    );
    await userEvent.click(screen.getAllByRole("button", { name: "schemaForm.remove" })[0]);
    expect(keyBoxes()).toHaveLength(1);
    expect(onChange.mock.calls.at(-1)?.[0]).toMatchObject({ env: { B: "2" } });
  });

  // Removing an environment variable asks first: the value is often a
  // ${secret.…} reference, so a mis-click costs a trip back to the secret
  // picker. Opt-in per field, not everywhere — see DictField.
  const confirmSchema: JSONSchema = {
    type: "object",
    properties: {
      env: {
        type: "object",
        title: "Environment variables",
        additionalProperties: { type: "string" },
        x_confirm_remove: true,
      },
    },
  } as JSONSchema;

  it("asks before removing, and keeps the row while asking", async () => {
    const onChange = vi.fn();
    render(
      <SchemaForm schema={confirmSchema} value={{ env: { API_TOKEN: "v" } }} onChange={onChange} />,
    );
    await userEvent.click(screen.getByRole("button", { name: "schemaForm.remove" }));

    // Nothing gone yet, and the row is still on screen — what is about to go
    // has to be visible while the question is being answered.
    expect(onChange).not.toHaveBeenCalled();
    expect(keyBoxes()).toHaveLength(1);
    // Named, so it is clear WHICH one.
    expect(screen.getByText(/schemaForm.dictRemoveConfirm.*API_TOKEN/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "common.remove" }));
    expect(onChange.mock.calls.at(-1)?.[0]).toMatchObject({ env: {} });
    expect(keyBoxes()).toHaveLength(0);
  });

  it("backs out of the removal on cancel", async () => {
    const onChange = vi.fn();
    render(
      <SchemaForm schema={confirmSchema} value={{ env: { API_TOKEN: "v" } }} onChange={onChange} />,
    );
    await userEvent.click(screen.getByRole("button", { name: "schemaForm.remove" }));
    await userEvent.click(screen.getByRole("button", { name: "common.cancel" }));
    expect(onChange).not.toHaveBeenCalled();
    expect(keyBoxes()).toHaveLength(1);
    expect(screen.queryByText(/schemaForm.dictRemoveConfirm/)).not.toBeInTheDocument();
  });

  it("asks about the row that was clicked, not another", async () => {
    render(
      <SchemaForm
        schema={confirmSchema}
        value={{ env: { FIRST: "1", SECOND: "2" } }}
        onChange={() => {}}
      />,
    );
    await userEvent.click(screen.getAllByRole("button", { name: "schemaForm.remove" })[1]);
    expect(screen.getByText(/schemaForm.dictRemoveConfirm.*SECOND/)).toBeInTheDocument();
    expect(screen.queryByText(/FIRST/)).not.toBeInTheDocument();
  });

  it("says so when one name is used twice", async () => {
    // The second wins when the object is built, so the first row's value
    // quietly is not what runs.
    render(
      <SchemaForm schema={dictSchema} value={{ env: { A: "1" } }} onChange={() => {}} />,
    );
    await userEvent.click(screen.getByRole("button", { name: /schemaForm.add/ }));
    await userEvent.type(keyBoxes()[1], "A");
    expect(keyBoxes()[1]).toHaveAttribute("aria-invalid", "true");
  });
});

// A multiline field becomes a code box when a sibling param says which language
// it is written in — the Text step's "Written in", the runner step's "Run it
// with". The field is told WHICH sibling to read, so the two steps can each ask
// the question in their own words.
describe("SchemaForm — a language-aware text box", () => {
  const textSchema: JSONSchema = {
    type: "object",
    properties: {
      text: { type: "string", format: "multiline", x_lang_param: "language", title: "Text" },
      language: { type: "string", title: "Written in", default: "plain", enum: ["plain", "sql"] },
    },
  } as JSONSchema;

  it("stays a plain textarea for prose", () => {
    // Most of what goes in one of these is a system prompt or an email body,
    // and prose in a monospace box reads worse, not better.
    const { container } = render(
      <SchemaForm schema={textSchema} value={{ text: "Dear team," }} onChange={() => {}} />,
    );
    expect(container.querySelector(".dz-code-editor")).toBeNull();
    expect(screen.getByDisplayValue("Dear team,")).toBeInTheDocument();
  });

  it("becomes a highlighted code box once a language is chosen", () => {
    const { container } = render(
      <SchemaForm
        schema={textSchema}
        value={{ text: "select id -- all", language: "sql" }}
        onChange={() => {}}
      />,
    );
    expect(container.querySelector(".dz-code-editor textarea")).toHaveValue("select id -- all");
    expect(container.querySelector(".dz-s-keyword")).toHaveTextContent("select");
    expect(container.querySelector(".dz-s-comment")).toHaveTextContent("-- all");
  });

  it("reads the sibling the schema names, not one it assumes", () => {
    // The runner step calls it `shell`; a field with no x_lang_param and no
    // script format must not pick up a stray sibling of either name.
    const noPointer: JSONSchema = {
      type: "object",
      properties: {
        text: { type: "string", format: "multiline", title: "Text" },
        language: { type: "string", enum: ["plain", "sql"] },
      },
    } as JSONSchema;
    const { container } = render(
      <SchemaForm schema={noPointer} value={{ text: "select 1", language: "sql" }} onChange={() => {}} />,
    );
    expect(container.querySelector(".dz-code-editor")).toBeNull();
  });
});

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

describe("SchemaForm — a two-way choice", () => {
  // Sort rows' direction, the field this rendering was added for.
  const toggleSchema: JSONSchema = {
    type: "object",
    properties: {
      sort_dir: {
        type: "string",
        format: "toggle",
        enum: ["asc", "desc"],
        enumNames: ["Ascending", "Descending"],
        default: "asc",
        title: "Direction",
      },
    },
  } as JSONSchema;

  it("puts both choices on screen instead of behind a dropdown", () => {
    render(<SchemaForm schema={toggleSchema} value={{}} onChange={() => {}} />);
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(screen.getByRole("button", { name: "Ascending" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Descending" })).toBeInTheDocument();
  });

  it("shows the default as chosen while the param is unset", () => {
    render(<SchemaForm schema={toggleSchema} value={{}} onChange={() => {}} />);
    expect(screen.getByRole("button", { name: "Ascending" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByRole("button", { name: "Descending" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );
  });

  it("follows the saved value, not the default", () => {
    render(<SchemaForm schema={toggleSchema} value={{ sort_dir: "desc" }} onChange={() => {}} />);
    expect(screen.getByRole("button", { name: "Descending" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("writes the picked enum value", async () => {
    const onChange = vi.fn();
    render(<SchemaForm schema={toggleSchema} value={{}} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: "Descending" }));
    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ sort_dir: "desc" }));
  });

  it("still renders a plain enum as a dropdown", () => {
    const selectSchema: JSONSchema = {
      type: "object",
      properties: {
        method: { type: "string", enum: ["GET", "POST"], title: "Method" },
      },
    } as JSONSchema;
    render(<SchemaForm schema={selectSchema} value={{}} onChange={() => {}} />);
    expect(screen.getByRole("combobox")).toBeInTheDocument();
  });
});

describe("SchemaForm — a name/value map with example boxes", () => {
  // render_table's column headings: the field a user reaches for to rename one
  // heading. Two unlabelled boxes and a "key" placeholder is what made the
  // same capability look absent when it was reachable only through the
  // canvas-side column editor.
  const mapSchema: JSONSchema = {
    type: "object",
    properties: {
      column_labels: {
        type: "object",
        additionalProperties: { type: "string" },
        title: "Column names",
        x_key_placeholder: "customer_email",
        x_value_placeholder: "Customer",
      },
    },
  } as JSONSchema;

  it("shows what goes in each box", async () => {
    render(<SchemaForm schema={mapSchema} value={{}} onChange={() => {}} />);
    await userEvent.click(screen.getByRole("button", { name: "schemaForm.add" }));
    expect(screen.getByPlaceholderText("customer_email")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("Customer")).toBeInTheDocument();
  });

  it("falls back to the generic placeholder when a map doesn't set one", async () => {
    const plain: JSONSchema = {
      type: "object",
      properties: {
        env: { type: "object", additionalProperties: { type: "string" }, title: "Env" },
      },
    } as JSONSchema;
    render(<SchemaForm schema={plain} value={{}} onChange={() => {}} />);
    await userEvent.click(screen.getByRole("button", { name: "schemaForm.add" }));
    expect(screen.getByPlaceholderText("schemaForm.keyPlaceholder")).toBeInTheDocument();
  });

  it("writes the pair into the params object", async () => {
    const onChange = vi.fn();
    render(<SchemaForm schema={mapSchema} value={{}} onChange={onChange} />);
    await userEvent.click(screen.getByRole("button", { name: "schemaForm.add" }));
    await userEvent.type(screen.getByPlaceholderText("customer_email"), "created_at");
    const last = onChange.mock.calls.at(-1)?.[0] as Record<string, unknown>;
    expect(Object.keys(last.column_labels as object)).toContain("created_at");
  });
});
