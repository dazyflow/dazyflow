// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The preview is rendered by the daemon and painted in a frame that can do
// nothing. Both halves of that matter: the render must be the step's own
// engine (so the picture is the truth), and the frame must never be able to
// run what it is showing.

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: "en" } }),
}));
vi.mock("../../auth", () => ({ useAuth: () => ({ token: "tok" }) }));

const previewRenderTemplate = vi.fn();
vi.mock("../../api", () => ({
  api: { previewRenderTemplate: (...a: unknown[]) => previewRenderTemplate(...a) },
}));

import { TemplateFrame, parseSample } from "./TemplateFrame";

const frame = () => document.querySelector("iframe");

describe("parseSample", () => {
  it("reads an empty box as empty data, not as a mistake", () => {
    expect(parseSample("   ")).toEqual({ data: {}, error: null });
  });

  it("reports what is wrong with half-typed JSON", () => {
    expect(parseSample('{"a":').error).not.toBeNull();
  });
});

describe("TemplateFrame", () => {
  beforeEach(() => {
    previewRenderTemplate.mockReset();
    previewRenderTemplate.mockResolvedValue({ html: "<h1>Hi Alex</h1>" });
  });

  it("renders through the daemon and paints the result in a sealed frame", async () => {
    render(<TemplateFrame template="<h1>Hi {{.name}}</h1>" sample='{"name":"Alex"}' />);
    // The call is debounced, so the frame is on screen before it is asked for.
    await waitFor(() =>
      expect(previewRenderTemplate).toHaveBeenCalledWith("tok", "<h1>Hi {{.name}}</h1>", {
        name: "Alex",
      }),
    );
    await waitFor(() => expect(frame()).toHaveAttribute("srcdoc", "<h1>Hi Alex</h1>"));
    // Empty sandbox: no scripts, no forms, no same-origin. Tenant-authored
    // markup cannot run in our origin.
    expect(frame()).toHaveAttribute("sandbox", "");
  });

  it("asks for nothing until there is a template to render", async () => {
    render(<TemplateFrame template="   " sample="{}" />);
    expect(screen.getByText("renderPreview.typeToPreview")).toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 400)); // past the debounce
    expect(previewRenderTemplate).not.toHaveBeenCalled();
  });

  it("holds off while the sample data is still being typed", async () => {
    render(<TemplateFrame template="<p>{{.a}}</p>" sample='{"a":' />);
    expect(screen.getByText("renderPreview.badJson")).toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 400));
    expect(previewRenderTemplate).not.toHaveBeenCalled();
  });

  it("shows a template error where the picture would be", async () => {
    previewRenderTemplate.mockResolvedValue({ error: "template: unclosed action" });
    render(<TemplateFrame template="{{if .x}}" sample="{}" />);
    await waitFor(() =>
      expect(screen.getByText("template: unclosed action")).toBeInTheDocument(),
    );
    expect(frame()).toBeNull();
  });
});
