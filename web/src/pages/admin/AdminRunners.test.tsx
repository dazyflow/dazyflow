// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// Stable `t` and auth objects: the page's load callback lists `t` in its deps,
// so a fresh function per render would re-fire it forever.
vi.mock("react-i18next", () => {
  const t = (k: string, o?: Record<string, unknown>) =>
    o && typeof o === "object" ? `${k}:${JSON.stringify(o)}` : k;
  const value = { t };
  return {
    useTranslation: () => value,
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../../auth", () => {
  const auth = { token: "tok", me: { tenant: "t", workspace: "ws" }, hasPerm: () => true };
  return { useAuth: () => auth };
});

const listRunners = vi.fn();
const putRunner = vi.fn();
const deleteRunner = vi.fn();
const testRunner = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    listRunners: (...a: unknown[]) => listRunners(...a),
    putRunner: (...a: unknown[]) => putRunner(...a),
    deleteRunner: (...a: unknown[]) => deleteRunner(...a),
    testRunner: (...a: unknown[]) => testRunner(...a),
  },
}));

import { AdminRunners } from "./AdminRunners";

// The page's job is to make a runner's real state legible: whether it is
// connected, what it offers, and — when it is not — why. A registered runner
// that will not connect is the case that matters, because the daemon keeps it
// listed precisely so this page can explain it.

const connected = {
  name: "invoices",
  endpoint: "runner.acme.internal:9000",
  enabled: true,
  created_at: "2026-08-01T10:00:00Z",
  updated_at: "2026-08-01T10:00:00Z",
  state: "connected" as const,
  drops: ["runner/invoices/fetch", "runner/invoices/render"],
};

const unreachable = {
  name: "billing",
  endpoint: "billing.acme.internal:9000",
  enabled: true,
  created_at: "2026-08-01T10:00:00Z",
  updated_at: "2026-08-01T10:00:00Z",
  state: "unreachable" as const,
  error: "tls: failed to verify certificate: x509: certificate has expired",
};

// Filling the form is what unlocks Test and Register, so most tests need it.
async function fillForm(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText("common.name"), "invoices");
  await user.type(screen.getByLabelText("runners.endpointLabel"), "host:9000");
  await user.type(screen.getByLabelText("runners.serverCertLabel"), "-----BEGIN CERTIFICATE-----");
  await user.type(screen.getByLabelText("runners.clientCertLabel"), "-----BEGIN CERTIFICATE-----");
  await user.type(screen.getByLabelText("runners.clientKeyLabel"), "-----BEGIN PRIVATE KEY-----");
}

beforeEach(() => {
  listRunners.mockResolvedValue({ runners: [] });
  putRunner.mockResolvedValue({});
  deleteRunner.mockResolvedValue({});
  testRunner.mockReset();
});

describe("AdminRunners", () => {
  it("offers a way in when nothing is registered", async () => {
    render(<AdminRunners />);
    expect(await screen.findByText("runners.emptyTitle")).toBeInTheDocument();
    // The form is always present, so an empty org has somewhere to start.
    expect(screen.getByText("runners.addTitle")).toBeInTheDocument();
  });

  it("shows what a connected runner offers", async () => {
    listRunners.mockResolvedValue({ runners: [connected] });
    render(<AdminRunners />);
    expect(await screen.findByText("invoices")).toBeInTheDocument();
    expect(screen.getByText("runners.state.connected")).toBeInTheDocument();
    // The steps, by their namespaced ids — which is what a flow references.
    expect(screen.getByText(/runner\/invoices\/fetch/)).toBeInTheDocument();
  });

  // The load-bearing case: a runner that will not connect stays listed WITH
  // its reason. If it vanished, the flow author would see a step they built
  // with simply not exist, and nothing would explain it.
  it("keeps an unreachable runner listed, with the reason", async () => {
    listRunners.mockResolvedValue({ runners: [unreachable] });
    render(<AdminRunners />);
    expect(await screen.findByText("billing")).toBeInTheDocument();
    expect(screen.getByText("runners.state.unreachable")).toBeInTheDocument();
    expect(screen.getByText(/certificate has expired/)).toBeInTheDocument();
  });

  it("warns before a certificate expires", async () => {
    listRunners.mockResolvedValue({
      runners: [{ ...connected, expiring_soon: true }],
    });
    render(<AdminRunners />);
    expect(await screen.findByText(/runners.expiringSoon/)).toBeInTheDocument();
  });

  it("does not warn when no certificate is near expiry", async () => {
    listRunners.mockResolvedValue({ runners: [connected] });
    render(<AdminRunners />);
    await screen.findByText("invoices");
    expect(screen.queryByText(/runners.expiringSoon/)).not.toBeInTheDocument();
  });

  // Registering needs every piece: a partly-filled form cannot be submitted,
  // because a runner missing one certificate can never connect.
  it("will not register until the form is complete", async () => {
    const user = userEvent.setup();
    render(<AdminRunners />);
    await screen.findByText("runners.addTitle");

    expect(screen.getByRole("button", { name: "runners.save" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "runners.test" })).toBeDisabled();

    await fillForm(user);
    expect(screen.getByRole("button", { name: "runners.save" })).toBeEnabled();
  });

  // Testing must not save. An admin checking pasted certificates would
  // otherwise leave a broken registration behind on every failed attempt.
  it("tests without registering", async () => {
    const user = userEvent.setup();
    testRunner.mockResolvedValue({ ok: true, subject: "CN=runner.acme.internal", drops: ["fetch"] });
    render(<AdminRunners />);
    await screen.findByText("runners.addTitle");
    await fillForm(user);

    await user.click(screen.getByRole("button", { name: "runners.test" }));
    await waitFor(() => expect(testRunner).toHaveBeenCalled());
    expect(putRunner).not.toHaveBeenCalled();
    // The subject, not just a tick: it is how an admin confirms whose runner
    // answered.
    expect(await screen.findByText("CN=runner.acme.internal")).toBeInTheDocument();
  });

  // A failed probe still reports who the certificate claims to be, because
  // that is the half the admin is checking.
  it("reports the identity even when the connection fails", async () => {
    const user = userEvent.setup();
    testRunner.mockResolvedValue({
      ok: false,
      subject: "CN=runner.acme.internal",
      error: "connection refused",
    });
    render(<AdminRunners />);
    await screen.findByText("runners.addTitle");
    await fillForm(user);

    await user.click(screen.getByRole("button", { name: "runners.test" }));
    expect(await screen.findByText("CN=runner.acme.internal")).toBeInTheDocument();
    expect(screen.getByText("connection refused")).toBeInTheDocument();
  });

  it("registers and reloads the list", async () => {
    const user = userEvent.setup();
    render(<AdminRunners />);
    await screen.findByText("runners.addTitle");
    await fillForm(user);

    listRunners.mockResolvedValue({ runners: [connected] });
    await user.click(screen.getByRole("button", { name: "runners.save" }));
    await waitFor(() => expect(putRunner).toHaveBeenCalled());
    expect(await screen.findByText("invoices")).toBeInTheDocument();

    // The key field is cleared: it is write-only server-side, so leaving it on
    // screen would imply it can be read back.
    expect(screen.getByLabelText("runners.clientKeyLabel")).toHaveValue("");
  });

  it("removes a runner", async () => {
    const user = userEvent.setup();
    listRunners.mockResolvedValue({ runners: [connected] });
    render(<AdminRunners />);
    await screen.findByText("invoices");

    await user.click(screen.getByRole("button", { name: "runners.remove" }));
    await waitFor(() => expect(deleteRunner).toHaveBeenCalledWith("tok", "invoices"));
  });

  it("surfaces a failure to load", async () => {
    listRunners.mockRejectedValue(new Error("nope"));
    render(<AdminRunners />);
    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });
});
