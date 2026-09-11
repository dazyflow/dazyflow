// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

// Stable `t` and auth: the page's load callback lists `t` in its deps, so a
// fresh function per render would re-fire it forever.
vi.mock("react-i18next", () => {
  const t = (k: string, o?: Record<string, unknown>) =>
    o && typeof o === "object" ? `${k}:${JSON.stringify(o)}` : k;
  const value = { t };
  return { useTranslation: () => value };
});
vi.mock("../../auth", () => {
  const auth = { token: "tok", hasPerm: () => true };
  return { useAuth: () => auth };
});

const listSSHCredentials = vi.fn();
const listSSHLogins = vi.fn();
const putSSHCredential = vi.fn();
const deleteSSHCredential = vi.fn();
const verifySSHCredential = vi.fn();
const scanSSHHostKey = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    listSSHCredentials: (...a: unknown[]) => listSSHCredentials(...a),
    listSSHLogins: (...a: unknown[]) => listSSHLogins(...a),
    putSSHCredential: (...a: unknown[]) => putSSHCredential(...a),
    deleteSSHCredential: (...a: unknown[]) => deleteSSHCredential(...a),
    verifySSHCredential: (...a: unknown[]) => verifySSHCredential(...a),
    scanSSHHostKey: (...a: unknown[]) => scanSSHHostKey(...a),
  },
}));

import { AdminSSHCredentials } from "./AdminSSHCredentials";

const DEPLOY = {
  name: "deploy-key",
  username: "deploy",
  has_password: false,
  has_ssh_key: true,
  has_passphrase: false,
};

beforeEach(() => {
  vi.clearAllMocks();
  listSSHCredentials.mockResolvedValue({ credentials: [] });
  listSSHLogins.mockResolvedValue({ logins: [DEPLOY] });
  putSSHCredential.mockResolvedValue(undefined);
  verifySSHCredential.mockResolvedValue({ ok: true });
});

const show = () => render(<MemoryRouter><AdminSSHCredentials /></MemoryRouter>);

async function fillMachine(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByPlaceholderText("web-1"), "web-1");
  await user.type(screen.getByPlaceholderText("ssh.example.com"), "ssh.example.com");
}

describe("AdminSSHCredentials — the machine, and the login it picks", () => {
  it("offers the org's logins by name and saves the one chosen", async () => {
    const user = userEvent.setup();
    show();
    await waitFor(() => expect(listSSHLogins).toHaveBeenCalled());
    await fillMachine(user);

    const picker = screen.getByRole("combobox");
    expect(screen.getByRole("option", { name: /deploy-key/ })).toBeTruthy();
    await user.selectOptions(picker, "deploy-key");

    await user.click(screen.getByText("sshCreds.saveBtn"));
    await waitFor(() => expect(putSSHCredential).toHaveBeenCalled());
    const [, account, body] = putSSHCredential.mock.calls[0];
    expect(account).toBe("web-1");
    expect(body.login).toBe("deploy-key");
    // The machine no longer carries a credential of its own.
    expect(body.private_key).toBeUndefined();
    expect(body.password).toBeUndefined();
  });

  // A server with no login signs in as nobody, so the form will not send one.
  it("will not save a server with no login chosen", async () => {
    const user = userEvent.setup();
    show();
    await waitFor(() => expect(listSSHLogins).toHaveBeenCalled());

    const save = () => screen.getByText("sshCreds.saveBtn").closest("button")!;
    await fillMachine(user);
    expect(save().disabled).toBe(true);
    await user.selectOptions(screen.getByRole("combobox"), "deploy-key");
    expect(save().disabled).toBe(false);
  });

  it("points at the logins page when there are none to pick", async () => {
    listSSHLogins.mockResolvedValue({ logins: [] });
    show();
    await waitFor(() => expect(listSSHLogins).toHaveBeenCalled());

    expect(screen.getByRole("option", { name: "sshCreds.noLogins" })).toBeTruthy();
    const link = screen.getByText("sshCreds.addLogin").closest("a")!;
    expect(link.getAttribute("href")).toBe("/admin/ssh-logins");
  });

  // The host key used to be learnable only by saving and reading the failure.
  it("fetches the host key from the server and pins it when accepted", async () => {
    const user = userEvent.setup();
    scanSSHHostKey.mockResolvedValue({
      ok: true,
      fingerprint: "SHA256:abc",
      key_type: "ssh-ed25519",
      known_hosts: "ssh.example.com ssh-ed25519 AAAA",
    });
    show();
    await waitFor(() => expect(listSSHLogins).toHaveBeenCalled());
    await fillMachine(user);
    await user.selectOptions(screen.getByRole("combobox"), "deploy-key");

    await user.click(screen.getByText("sshCreds.checkServer"));
    await waitFor(() => expect(scanSSHHostKey).toHaveBeenCalledWith("tok", "ssh.example.com", ""));
    // Shown, not accepted: the comparison is the operator's to make.
    expect(screen.getByText(/SHA256:abc/)).toBeTruthy();
    expect(screen.queryByText(/sshCreds.pinned/)).toBeNull();

    await user.click(screen.getByText("sshCreds.acceptKey"));
    expect(screen.getByText(/sshCreds.pinned/)).toBeTruthy();

    await user.click(screen.getByText("sshCreds.saveBtn"));
    await waitFor(() => expect(putSSHCredential).toHaveBeenCalled());
    const body = putSSHCredential.mock.calls[0][2];
    expect(body.fingerprint).toBe("SHA256:abc");
    expect(body.known_hosts).toBe("ssh.example.com ssh-ed25519 AAAA");
  });

  it("reports a server that cannot be reached instead of pinning nothing", async () => {
    const user = userEvent.setup();
    scanSSHHostKey.mockResolvedValue({ ok: false, error: "couldn't reach ssh.example.com:22" });
    show();
    await waitFor(() => expect(listSSHLogins).toHaveBeenCalled());
    await fillMachine(user);

    await user.click(screen.getByText("sshCreds.checkServer"));
    await waitFor(() => expect(screen.getByText(/couldn't reach/)).toBeTruthy());
    expect(screen.queryByText(/sshCreds.pinned/)).toBeNull();
  });

  // Saving is not the same as working, and that is what people come back for.
  it("tries the connection straight after saving", async () => {
    const user = userEvent.setup();
    show();
    await waitFor(() => expect(listSSHLogins).toHaveBeenCalled());
    await fillMachine(user);
    await user.selectOptions(screen.getByRole("combobox"), "deploy-key");

    await user.click(screen.getByText("sshCreds.saveBtn"));
    await waitFor(() => expect(verifySSHCredential).toHaveBeenCalledWith("tok", "web-1"));
  });

  // Servers saved before the split still carry their own key; the list says so
  // rather than showing a blank where a login would be.
  it("marks a pre-split server as carrying its own login", async () => {
    listSSHCredentials.mockResolvedValue({
      credentials: [
        { account: "old-1", host: "10.0.0.9", username: "root", has_password: true, has_ssh_key: false, has_passphrase: false, has_host_key: true },
      ],
    });
    show();
    await waitFor(() => expect(listSSHCredentials).toHaveBeenCalled());
    expect(screen.getByText(/sshCreds.ownLogin/)).toBeTruthy();
  });
});
