// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// The flow-settings modal's save contract. The bug this covers was reported as
// "I switch the language to Swedish, press Save, reopen settings, and it still
// says English" — a save that silently kept the previous value.

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
  Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
}));
vi.mock("../../auth", () => ({
  useAuth: () => ({ hasPerm: () => true, token: "tok" }),
}));

import { SettingsModal } from "./SettingsModal";
import type { Graph } from "../../types";

const graph: Graph = {
  id: "coffee-reorder",
  tenant: "acme",
  workspace: "main",
  nodes: [],
  edges: [],
  name: "Coffee reorder",
};

function open(g: Graph = graph) {
  const onSave = vi.fn();
  render(
    <MemoryRouter>
      <SettingsModal graph={g} onClose={() => {}} onSave={onSave} onDelete={undefined} />
    </MemoryRouter>,
  );
  return onSave;
}

const generalTab = async () => {
  await userEvent.click(screen.getByText("settings.tabGeneral"));
};

describe("SettingsModal flow language", () => {
  it("saves the language that was picked", async () => {
    const onSave = open();
    await generalTab();
    const select = screen.getByLabelText("settings.general.language");
    await userEvent.selectOptions(select, "sv");
    await userEvent.click(screen.getByText("common.save"));
    expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ language: "sv" }));
  });

  it("shows the language the flow already has when reopened", async () => {
    open({ ...graph, language: "sv" });
    await generalTab();
    expect(screen.getByLabelText("settings.general.language")).toHaveValue("sv");
  });

  // Switching back to English clears the field rather than writing "en": empty
  // IS English on the Go side, and a flow shouldn't grow a field to say so.
  it("clears the field when switched back to English", async () => {
    const onSave = open({ ...graph, language: "sv" });
    await generalTab();
    await userEvent.selectOptions(
      screen.getByLabelText("settings.general.language"),
      "",
    );
    await userEvent.click(screen.getByText("common.save"));
    const saved = onSave.mock.calls[0][0] as Graph;
    expect(saved.language).toBeUndefined();
  });
});
