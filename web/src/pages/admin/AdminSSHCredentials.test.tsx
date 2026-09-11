// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

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
const putSSHCredential = vi.fn();
const deleteSSHCredential = vi.fn();
const verifySSHCredential = vi.fn();
const scanSSHHostKey = vi.fn();
const generateSSHKey = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    listSSHCredentials: (...a: unknown[]) => listSSHCredentials(...a),
    putSSHCredential: (...a: unknown[]) => putSSHCredential(...a),
    deleteSSHCredential: (...a: unknown[]) => deleteSSHCredential(...a),
    verifySSHCredential: (...a: unknown[]) => verifySSHCredential(...a),
    scanSSHHostKey: (...a: unknown[]) => scanSSHHostKey(...a),
    generateSSHKey: (...a: unknown[]) => generateSSHKey(...a),
  },
}));

import { AdminSSHCredentials } from "./AdminSSHCredentials";

const KEY = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk=\n-----END OPENSSH PRIVATE KEY-----";

beforeEach(() => {
  vi.clearAllMocks();
  listSSHCredentials.mockResolvedValue({ credentials: [] });
  putSSHCredential.mockResolvedValue(undefined);
  verifySSHCredential.mockResolvedValue({ ok: true });
});

async function fillServer(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByPlaceholderText("web-1"), "web-1");
  await user.type(screen.getByPlaceholderText("ssh.example.com"), "ssh.example.com");
  await user.type(screen.getByPlaceholderText("deploy"), "deploy");
}

describe("AdminSSHCredentials — the setup guide", () => {
  it("asks the sign-in question once, with the three named answers", async () => {
    render(<AdminSSHCredentials />);
    await waitFor(() => expect(listSSHCredentials).toHaveBeenCalled());

    expect(screen.getByText("sshCreds.wayPaste")).toBeTruthy();
    expect(screen.getByText("sshCreds.wayFile")).toBeTruthy();
    expect(screen.getByText("sshCreds.wayGenerate")).toBeTruthy();
    // Nothing is assumed: no key box and no password box until one is picked.
    expect(screen.queryByPlaceholderText(/BEGIN OPENSSH PRIVATE KEY/)).toBeNull();
  });

  it("opens the key box only once Paste is chosen", async () => {
    const user = userEvent.setup();
    render(<AdminSSHCredentials />);
    await waitFor(() => expect(listSSHCredentials).toHaveBeenCalled());

    await user.click(screen.getByText("sshCreds.wayPaste"));
    expect(screen.getByPlaceholderText(/BEGIN OPENSSH PRIVATE KEY/)).toBeTruthy();
  });

  // The generated private half is the one secret the page must never render:
  // it goes straight from the response into the save.
  it("shows the public half of a generated key, never the private half", async () => {
    const user = userEvent.setup();
    generateSSHKey.mockResolvedValue({
      private_key: KEY,
      public_key: "ssh-ed25519 AAAAC3NzaC1lZDI1 dazyflow web-1",
    });
    render(<AdminSSHCredentials />);
    await waitFor(() => expect(listSSHCredentials).toHaveBeenCalled());
    await fillServer(user);

    await user.click(screen.getByText("sshCreds.wayGenerate"));
    await waitFor(() => expect(generateSSHKey).toHaveBeenCalledWith("tok", "dazyflow web-1"));
    expect(screen.getByText("ssh-ed25519 AAAAC3NzaC1lZDI1 dazyflow web-1")).toBeTruthy();
    expect(document.body.textContent).not.toContain("BEGIN OPENSSH PRIVATE KEY");

    await user.click(screen.getByText("sshCreds.saveBtn"));
    await waitFor(() => expect(putSSHCredential).toHaveBeenCalled());
    expect(putSSHCredential.mock.calls[0][2].private_key).toBe(KEY);
  });

  // The host key used to be learnable only by saving a credential and reading
  // the fingerprint out of the failure.
  it("fetches the host key from the server and pins it when accepted", async () => {
    const user = userEvent.setup();
    scanSSHHostKey.mockResolvedValue({
      ok: true,
      fingerprint: "SHA256:abc",
      key_type: "ssh-ed25519",
      known_hosts: "ssh.example.com ssh-ed25519 AAAA",
    });
    render(<AdminSSHCredentials />);
    await waitFor(() => expect(listSSHCredentials).toHaveBeenCalled());
    await fillServer(user);
    await user.click(screen.getByText("sshCreds.wayPaste"));
    await user.type(screen.getByPlaceholderText(/BEGIN OPENSSH PRIVATE KEY/), KEY);

    await user.click(screen.getByText("sshCreds.checkServer"));
    await waitFor(() =>
      expect(scanSSHHostKey).toHaveBeenCalledWith("tok", "ssh.example.com", ""),
    );
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
    render(<AdminSSHCredentials />);
    await waitFor(() => expect(listSSHCredentials).toHaveBeenCalled());
    await fillServer(user);

    await user.click(screen.getByText("sshCreds.checkServer"));
    await waitFor(() => expect(screen.getByText(/couldn't reach/)).toBeTruthy());
    expect(screen.queryByText(/sshCreds.pinned/)).toBeNull();
  });

  // Saving is not the same as working, and that is what people come back for.
  it("tries the connection straight after saving", async () => {
    const user = userEvent.setup();
    const { container } = render(<AdminSSHCredentials />);
    await waitFor(() => expect(listSSHCredentials).toHaveBeenCalled());
    await fillServer(user);
    await user.click(screen.getByText("sshCreds.wayPassword"));
    // The password way renders exactly one password box — no passphrase beside it.
    const boxes = container.querySelectorAll('input[type="password"]');
    expect(boxes).toHaveLength(1);
    await user.type(boxes[0], "hunter2");

    await user.click(screen.getByText("sshCreds.saveBtn"));
    await waitFor(() => expect(verifySSHCredential).toHaveBeenCalledWith("tok", "web-1"));
  });

  it("will not save until a server, a login and a way in are all there", async () => {
    const user = userEvent.setup();
    render(<AdminSSHCredentials />);
    await waitFor(() => expect(listSSHCredentials).toHaveBeenCalled());

    const save = () => screen.getByText("sshCreds.saveBtn").closest("button")!;
    expect(save().disabled).toBe(true);
    await fillServer(user);
    expect(save().disabled).toBe(true); // still no way to sign in
    await user.click(screen.getByText("sshCreds.wayPaste"));
    await user.type(screen.getByPlaceholderText(/BEGIN OPENSSH PRIVATE KEY/), KEY);
    expect(save().disabled).toBe(false);
  });
});
