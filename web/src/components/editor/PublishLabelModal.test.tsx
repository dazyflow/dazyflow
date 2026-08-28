// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PublishLabelModal } from "./PublishLabelModal";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

function renderModal(
  overrides: Partial<Parameters<typeof PublishLabelModal>[0]> = {},
) {
  const onPublish = vi.fn();
  const onCancel = vi.fn();
  render(
    <PublishLabelModal
      title="Go live with this flow?"
      message="Automatic triggers will run it."
      confirmLabel="Go live"
      onPublish={onPublish}
      onCancel={onCancel}
      {...overrides}
    />,
  );
  return { onPublish, onCancel };
}

describe("PublishLabelModal", () => {
  it("publishes with the typed label, and unnamed from the skip button", async () => {
    const { onPublish } = renderModal();
    await userEvent.type(screen.getByRole("textbox"), "  Black Friday  ");
    await userEvent.click(screen.getByRole("button", { name: "Go live" }));
    expect(onPublish).toHaveBeenCalledWith("Black Friday");

    await userEvent.click(
      screen.getByRole("button", { name: "editor.publishWithoutName" }),
    );
    expect(onPublish).toHaveBeenLastCalledWith("");
  });

  // The default (healthy flow) shape: no warning, and publishing is the
  // emphasised action. Guards against the gate below leaking into every
  // publish.
  it("shows no warning and keeps publish primary when nothing is missing", () => {
    renderModal();
    expect(screen.queryByRole("note")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Go live" })).toHaveClass("primary");
    expect(screen.getByRole("dialog")).not.toHaveClass("publish-gate");
  });

  // The gate: going live with an unconnected app arms triggers for runs that
  // cannot succeed, so the warning must name the gap and connecting — not
  // publishing — must be the emphasised action.
  it("warns and demotes publish when a connection is missing", () => {
    renderModal({
      warning: "Email (SMTP) still needs setting up.",
      connect: { label: "Go to Connections", onClick: vi.fn() },
    });
    expect(
      screen.getByText("Email (SMTP) still needs setting up."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Go live" })).not.toHaveClass(
      "primary",
    );
    expect(
      screen.getByRole("button", { name: "Go to Connections" }),
    ).toHaveClass("primary");
    expect(screen.getByRole("dialog")).toHaveClass("publish-gate");
  });

  // Publishing anyway stays reachable — the missing-connection check is the
  // same heuristic the run gate uses, so it must warn, not block outright.
  it("still allows publishing past the warning", async () => {
    const onConnect = vi.fn();
    const { onPublish } = renderModal({
      warning: "Email (SMTP) still needs setting up.",
      connect: { label: "Go to Connections", onClick: onConnect },
    });
    await userEvent.click(screen.getByRole("button", { name: "Go live" }));
    expect(onPublish).toHaveBeenCalledWith("");
    expect(onConnect).not.toHaveBeenCalled();
  });

  it("cancels on Escape", async () => {
    const { onCancel } = renderModal();
    await userEvent.keyboard("{Escape}");
    expect(onCancel).toHaveBeenCalledOnce();
  });
});
