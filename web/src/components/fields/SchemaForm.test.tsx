// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));
// nodeCardShared imports the real i18n singleton (which inits a browser
// language detector that doesn't run in jsdom); stub it to a plain t().
vi.mock("../../i18n", () => ({ default: { t: (k: string) => k } }));
vi.mock("../../auth", () => ({
  useAuth: () => ({ token: null, hasPerm: () => false }),
}));

import { SchemaForm } from "./SchemaForm";
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
