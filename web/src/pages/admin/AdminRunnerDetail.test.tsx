// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import userEvent from "@testing-library/user-event";

// t() returns the key with {{interpolations}} filled in, so a test can assert on
// the part of a label that comes from the data without pulling the real
// catalogues in.
vi.mock("react-i18next", () => {
  const t = (k: string, o?: Record<string, unknown>) =>
    o && typeof o === "object" ? `${k}:${JSON.stringify(o)}` : k;
  const value = { t };
  return { useTranslation: () => value, Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</> };
});
vi.mock("../../auth", () => {
  const auth = { token: "tok", me: { tenant: "t", workspace: "ws" }, hasPerm: () => true };
  return { useAuth: () => auth };
});

const listRunners = vi.fn();
const setRunnerLabels = vi.fn();
const mintRunnerToken = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    listRunners: (...a: unknown[]) => listRunners(...a),
    setRunnerLabels: (...a: unknown[]) => setRunnerLabels(...a),
    mintRunnerToken: (...a: unknown[]) => mintRunnerToken(...a),
  },
}));

import { AdminRunnerDetail } from "./AdminRunnerDetail";

// This page exists so that clicking a machine lands somewhere its tags can be
// changed. A tag is what decides which steps send work here, so most of these
// tests are about the tag set being editable AND honestly reported: what the
// server accepted, not what was typed.

const box = {
  name: "invoices-box",
  labels: ["build", "linux"],
  version: "0.2.0",
  online: true,
  last_seen: "2026-08-25T09:00:00Z",
  created_by: "ada@acme.test",
  created_at: "2026-08-01T10:00:00Z",
};

const mount = (name = "invoices-box") =>
  render(
    <MemoryRouter initialEntries={[`/admin/runners/${name}`]}>
      <Routes>
        <Route path="/admin/runners/:name" element={<AdminRunnerDetail />} />
      </Routes>
    </MemoryRouter>,
  );

beforeEach(() => {
  mintRunnerToken.mockReset();
  listRunners.mockResolvedValue({ runners: [box] });
  setRunnerLabels.mockImplementation((_tok: string, name: string, labels: string[]) =>
    // The server normalizes and returns the saved row; the page shows what came
    // back rather than what was typed.
    Promise.resolve({
      ...box,
      name,
      labels: [...new Set(labels.map((l) => l.trim().toLowerCase()))].sort(),
    }),
  );
});

describe("AdminRunnerDetail", () => {
  it("shows the machine, its state and its tags", async () => {
    mount();
    expect(await screen.findByRole("heading", { name: "invoices-box" })).toBeInTheDocument();
    expect(screen.getByText("runners.online")).toBeInTheDocument();
    expect(screen.getByText("0.2.0")).toBeInTheDocument();
    expect(screen.getByText("build")).toBeInTheDocument();
    expect(screen.getByText("linux")).toBeInTheDocument();
  });

  // The name being a tag is what lets a step target ONE machine now that there
  // is no separate "which machine" field. Invisible unless the page shows it,
  // so it sits with the others — and has no remove button, because there is no
  // such thing as a machine without its own name.
  it("shows the machine's name as a tag it always carries", async () => {
    const { container } = mount();
    await screen.findByRole("heading", { name: "invoices-box" });
    const nameTag = container.querySelector(".runner-tag.is-name");
    expect(nameTag).toHaveTextContent("invoices-box");
    expect(nameTag?.querySelector("button")).toBeNull();
    expect(
      screen.queryByRole("button", { name: /runners.tagRemove.*invoices-box/ }),
    ).not.toBeInTheDocument();
  });

  it("assigns a tag", async () => {
    const user = userEvent.setup();
    mount();
    await screen.findByRole("heading", { name: "invoices-box" });

    await user.type(screen.getByLabelText("runners.tagPlaceholder"), "GPU ");
    await user.click(screen.getByRole("button", { name: /runners.tagAdd/ }));

    // The whole set goes up, not a diff: the set is what routes work, so two
    // admins editing one machine each end with a set they meant.
    await waitFor(() =>
      expect(setRunnerLabels).toHaveBeenCalledWith("tok", "invoices-box", ["build", "linux", "GPU"]),
    );
    // And the page shows the server's normalized answer, so it is visible that
    // a step has to spell it "gpu".
    expect(await screen.findByText("gpu")).toBeInTheDocument();
  });

  it("removes a tag", async () => {
    const user = userEvent.setup();
    mount();
    await screen.findByRole("heading", { name: "invoices-box" });

    await user.click(screen.getByRole("button", { name: /runners.tagRemove.*build/ }));
    await waitFor(() =>
      expect(setRunnerLabels).toHaveBeenCalledWith("tok", "invoices-box", ["linux"]),
    );
  });

  it("does not spend a request on a tag the machine already carries", async () => {
    const user = userEvent.setup();
    mount();
    await screen.findByRole("heading", { name: "invoices-box" });

    // Including its own name, which it carries by definition — the common slip
    // now that the name is a tag.
    await user.type(screen.getByLabelText("runners.tagPlaceholder"), "LINUX{Enter}");
    await user.type(screen.getByLabelText("runners.tagPlaceholder"), "invoices-box{Enter}");
    expect(setRunnerLabels).not.toHaveBeenCalled();
  });

  // A refused tag (a comma, another machine's name, a seventeenth pool) must not
  // look as though it stuck: the failure surfaces and the chips keep showing
  // what the server still holds.
  it("says when a tag was refused, and keeps the old set", async () => {
    const user = userEvent.setup();
    setRunnerLabels.mockRejectedValue(new Error("another machine's name"));
    mount();
    await screen.findByRole("heading", { name: "invoices-box" });

    await user.type(screen.getByLabelText("runners.tagPlaceholder"), "other-box{Enter}");
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.getByText("build")).toBeInTheDocument();
    expect(screen.queryByText("other-box")).not.toBeInTheDocument();
  });

  it("says so when the machine is not registered any more", async () => {
    // A bookmark, or a browser back after removing it. Naming the machine beats
    // an empty page.
    listRunners.mockResolvedValue({ runners: [] });
    mount("ghost-box");
    expect(await screen.findByText(/runners.notFound/)).toBeInTheDocument();
  });

  it("offers a way back to the list", async () => {
    mount();
    await screen.findByRole("heading", { name: "invoices-box" });
    expect(screen.getByRole("link", { name: /runners.title/ })).toHaveAttribute(
      "href",
      "/admin/runners",
    );
  });

  // Re-registering mints a token PINNED to this machine's name — the only kind
  // that may replace a live runner — and the install command names the machine
  // explicitly, so a rebuilt host with a different hostname still reclaims it.
  it("mints a token pinned to this machine and names it in the command", async () => {
    mintRunnerToken.mockResolvedValue({
      token: "dzrt_pinned",
      expires_at: "2026-08-27T10:30:00Z",
      name: "invoices-box",
    });
    mount();
    await screen.findByRole("heading", { name: "invoices-box" });

    await userEvent.click(screen.getByRole("button", { name: /runners.reregister/ }));

    // The name reached the API, so the token can overwrite this runner.
    await waitFor(() => expect(mintRunnerToken).toHaveBeenCalledWith("tok", "invoices-box"));
    // And the command carries --name, not just --token.
    const cmd = await screen.findByText(/--token dzrt_pinned --name invoices-box --service/);
    expect(cmd).toBeInTheDocument();
  });
});
