// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The inspector's Console panel, and which of the two sources it draws from.
// The live stream is best-effort and ends with the run; the step's own record
// is what is left afterwards. A reader should not have to know which one they
// are looking at, so the panel has to pick — and picking wrong means a console
// that empties itself the moment the run finishes.

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { Node } from "@xyflow/react";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "en" } }),
  initReactI18next: { type: "3rdParty", init: () => {} },
  Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
}));
vi.mock("../../i18n", () => ({
  default: { language: "en", t: (k: string) => k },
}));
vi.mock("../../auth", () => ({
  useAuth: () => ({ token: "tok", hasPerm: () => true }),
}));
// xterm draws on a canvas, so the rendered terminal asserts nothing. The stub
// keeps the lines in the DOM, which is the part this test is about.
vi.mock("./LiveConsole", () => ({
  LiveConsole: ({ lines }: { lines: string[] }) => (
    <pre data-testid="console">{lines.join("\n")}</pre>
  ),
}));

import { Inspector } from "./Inspector";
import type { DazyNodeData } from "./nodeCardShared";

const node = {
  id: "code_1",
  position: { x: 0, y: 0 },
  data: { label: "Code", moduleID: "code" },
} as Node<DazyNodeData>;

function renderInspector(props: {
  liveLogs?: string[];
  recordedLogs?: string[];
}) {
  return render(
    <MemoryRouter>
      <Inspector
        selected={node}
        onChange={() => {}}
        paramsByID={{}}
        onParamsChange={() => {}}
        currentRunID={null}
        {...props}
      />
    </MemoryRouter>,
  );
}

describe("the inspector console", () => {
  it("shows what the step recorded once the stream has nothing", () => {
    renderInspector({ recordedLogs: ["  1 │ hello"] });
    expect(screen.getByTestId("console")).toHaveTextContent("1 │ hello");
  });

  it("prefers the live lines while a run is streaming", () => {
    renderInspector({ liveLogs: ["live"], recordedLogs: ["recorded"] });
    expect(screen.getByTestId("console")).toHaveTextContent("live");
    expect(screen.getByTestId("console")).not.toHaveTextContent("recorded");
  });

  it("stays out of the way when the step printed nothing", () => {
    renderInspector({ recordedLogs: [] });
    expect(screen.queryByTestId("console")).toBeNull();
  });
});
