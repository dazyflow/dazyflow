// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

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

const listSSHLogins = vi.fn();
const putSSHLogin = vi.fn();
const deleteSSHLogin = vi.fn();
const generateSSHKey = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    listSSHLogins: (...a: unknown[]) => listSSHLogins(...a),
    putSSHLogin: (...a: unknown[]) => putSSHLogin(...a),
    deleteSSHLogin: (...a: unknown[]) => deleteSSHLogin(...a),
    generateSSHKey: (...a: unknown[]) => generateSSHKey(...a),
  },
}));

import { AdminSSHLogins } from "./AdminSSHLogins";

const KEY = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXk=\n-----END OPENSSH PRIVATE KEY-----";
const PUB = "ssh-ed25519 AAAAC3NzaC1lZDI1 dazyflow deploy-key";

beforeEach(() => {
  vi.clearAllMocks();
  listSSHLogins.mockResolvedValue({ logins: [] });
  putSSHLogin.mockResolvedValue(undefined);
});

const show = () => render(<MemoryRouter><AdminSSHLogins /></MemoryRouter>);

async function name(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByPlaceholderText("deploy-key"), "deploy-key");
  await user.type(screen.getByPlaceholderText("deploy"), "deploy");
}

describe("AdminSSHLogins — who signs in", () => {
  it("asks the question once, with the three named answers", async () => {
    show();
    await waitFor(() => expect(listSSHLogins).toHaveBeenCalled());

    expect(screen.getByText("sshCreds.wayPaste")).toBeTruthy();
    expect(screen.getByText("sshCreds.wayFile")).toBeTruthy();
    expect(screen.getByText("sshCreds.wayGenerate")).toBeTruthy();
    // Nothing is assumed: no key box until one is picked.
    expect(screen.queryByPlaceholderText(/BEGIN OPENSSH PRIVATE KEY/)).toBeNull();
  });

  it("saves a pasted key against a username", async () => {
    const user = userEvent.setup();
    show();
    await waitFor(() => expect(listSSHLogins).toHaveBeenCalled());
    await name(user);

    await user.click(screen.getByText("sshCreds.wayPaste"));
    await user.type(screen.getByPlaceholderText(/BEGIN OPENSSH PRIVATE KEY/), KEY);
    await user.click(screen.getByText("sshLogins.saveBtn"));

    await waitFor(() => expect(putSSHLogin).toHaveBeenCalled());
    const [, login, body] = putSSHLogin.mock.calls[0];
    expect(login).toBe("deploy-key");
    expect(body.username).toBe("deploy");
    expect(body.private_key).toBe(KEY);
  });

  // The generated private half is the one secret the page must never render;
  // the public half is kept so every later server can be given the same line.
  it("shows the public half of a generated key and stores it, never the private half", async () => {
    const user = userEvent.setup();
    generateSSHKey.mockResolvedValue({ private_key: KEY, public_key: PUB });
    show();
    await waitFor(() => expect(listSSHLogins).toHaveBeenCalled());
    await name(user);

    await user.click(screen.getByText("sshCreds.wayGenerate"));
    await waitFor(() => expect(generateSSHKey).toHaveBeenCalledWith("tok", "dazyflow deploy-key"));
    expect(screen.getByText(PUB)).toBeTruthy();
    expect(document.body.textContent).not.toContain("BEGIN OPENSSH PRIVATE KEY");

    await user.click(screen.getByText("sshLogins.saveBtn"));
    await waitFor(() => expect(putSSHLogin).toHaveBeenCalled());
    const body = putSSHLogin.mock.calls[0][2];
    expect(body.private_key).toBe(KEY);
    expect(body.public_key).toBe(PUB);
  });

  // A login IS its key line, so the list leads with it: that is what has to be
  // carried to each server's authorized_keys.
  it("shows a saved login's public key with a way to copy it", async () => {
    listSSHLogins.mockResolvedValue({
      logins: [{ name: "deploy-key", username: "deploy", public_key: PUB, has_password: false, has_ssh_key: true, has_passphrase: false }],
    });
    show();
    await waitFor(() => expect(listSSHLogins).toHaveBeenCalled());

    expect(screen.getByText(PUB)).toBeTruthy();
    expect(screen.getByText("common.copy")).toBeTruthy();
  });

  it("will not save until a name, a username and a way in are all there", async () => {
    const user = userEvent.setup();
    show();
    await waitFor(() => expect(listSSHLogins).toHaveBeenCalled());

    const save = () => screen.getByText("sshLogins.saveBtn").closest("button")!;
    expect(save().disabled).toBe(true);
    await name(user);
    expect(save().disabled).toBe(true); // still no key and no password
    await user.click(screen.getByText("sshCreds.wayPaste"));
    await user.type(screen.getByPlaceholderText(/BEGIN OPENSSH PRIVATE KEY/), KEY);
    expect(save().disabled).toBe(false);
  });
});
