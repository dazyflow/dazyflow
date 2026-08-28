// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// One stable `t`, deliberately. i18next's real t is referentially stable, and
// the components here follow the codebase pattern of listing it in a
// useCallback dep list — so a mock that returns a fresh function per render
// would spin the load effect forever and test nothing but the mock.
const tStub = (k: string, vars?: Record<string, unknown>) =>
  vars ? `${k}:${JSON.stringify(vars)}` : k;
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: tStub }),
}));
vi.mock("../auth", () => ({
  useAuth: () => ({ token: "tok-123", hasPerm: () => true }),
}));
vi.mock("../lib/datetime", () => ({
  formatRelative: () => "2 minutes ago",
  formatDateTime: (v: string) => v,
}));

const getGitMirror = vi.fn();
const putGitMirror = vi.fn();
const deleteGitMirror = vi.fn();
const pushGitMirror = vi.fn();
// APIError has to be part of the mock: the panel branches on
// `e instanceof APIError && e.status === 409` to tell the overwrite-confirm
// case from a real fault, and a mock without it makes that check throw.
//
// Declared through vi.hoisted because vi.mock's factory is lifted above every
// top-level statement — a plain class declaration up here is not yet
// initialised when the factory runs.
const { MockAPIError } = vi.hoisted(() => {
  class MockAPIError extends Error {
    status: number;
    constructor(status: number, message: string) {
      super(message);
      this.status = status;
    }
  }
  return { MockAPIError };
});
vi.mock("../api", () => ({
  APIError: MockAPIError,
  api: {
    getGitMirror: (...a: unknown[]) => getGitMirror(...a),
    putGitMirror: (...a: unknown[]) => putGitMirror(...a),
    deleteGitMirror: (...a: unknown[]) => deleteGitMirror(...a),
    pushGitMirror: (...a: unknown[]) => pushGitMirror(...a),
  },
}));
vi.mock("../lib/explainApiError", () => ({
  explainApiError: (e: unknown) => String((e as Error).message),
}));

import { GitMirrorPanel } from "./GitMirrorPanel";
import type { GitCredential, GitMirror } from "../types";

const sshCred: GitCredential = {
  account: "deploy",
  has_ssh_key: true,
  has_passphrase: false,
  has_known_hosts: false,
  has_token: false,
};
const patOnlyCred: GitCredential = {
  account: "github-pat",
  has_ssh_key: false,
  has_passphrase: false,
  has_known_hosts: false,
  has_token: true,
};

const configured: GitMirror = {
  configured: true,
  remote_url: "git@github.com:acme/flows.git",
  account: "deploy",
  enabled: true,
  push_on: "publish",
  last_attempt_at: "2026-08-21T10:00:00Z",
  last_success_at: "2026-08-21T10:00:00Z",
  last_commit: "abcdef1234567890",
};

// renderPanel renders and waits for the initial GET to land. Waiting on the
// spy being CALLED isn't enough — the component is still showing its loading
// card at that point, so every query below would miss.
async function renderPanel(credentials: GitCredential[]) {
  const view = render(<GitMirrorPanel credentials={credentials} />);
  await screen.findByText("gitMirror.title");
  return view;
}

describe("GitMirrorPanel", () => {
  beforeEach(() => {
    getGitMirror.mockReset().mockResolvedValue({ configured: false, enabled: false });
    putGitMirror.mockReset().mockResolvedValue(configured);
    deleteGitMirror.mockReset().mockResolvedValue({ configured: false, enabled: false });
    pushGitMirror.mockReset().mockResolvedValue({
      pushed: 2,
      deleted: 0,
      changed: true,
      commit: "abcdef1234567890",
    });
  });

  // The mirror pushes over SSH only, so a workspace whose credentials are all
  // PAT-only can't configure one. Showing the form anyway would mean every
  // submission 400s — the panel has to say what's missing instead.
  it("refuses to render the form when no credential has an SSH key", async () => {
    await renderPanel([patOnlyCred]);
    await screen.findByText("gitMirror.needSSHKey", { exact: false });
    expect(screen.queryByLabelText("gitMirror.remoteLabel")).not.toBeInTheDocument();
  });

  it("offers only SSH-capable credentials in the picker", async () => {
    await renderPanel([sshCred, patOnlyCred]);
    const options = screen.getAllByRole("option").map((o) => o.textContent);
    expect(options).toContain("deploy");
    expect(options).not.toContain("github-pat");
  });

  it("saves the configuration the user entered", async () => {
    await renderPanel([sshCred]);

    const url = screen.getByPlaceholderText("git@github.com:acme/dazyflow-flows.git");
    await userEvent.type(url, "git@github.com:acme/flows.git");
    await userEvent.click(screen.getByRole("button", { name: "gitMirror.saveBtn" }));

    expect(putGitMirror).toHaveBeenCalledWith("tok-123", {
      remote_url: "git@github.com:acme/flows.git",
      // Defaulted: with exactly one SSH credential, making the user pick it
      // would be pure friction.
      account: "deploy",
      enabled: false,
      push_on: "publish",
    });
  });

  // Push now must work on a saved-but-disabled mirror: that is the whole
  // point of it — verify the remote and the key BEFORE turning the mirror on.
  it("allows Push now while automatic mirroring is off", async () => {
    getGitMirror.mockResolvedValue({ ...configured, enabled: false });
    await renderPanel([sshCred]);

    const push = screen.getByRole("button", { name: /gitMirror\.pushNow/ });
    expect(push).not.toBeDisabled();
    await userEvent.click(push);
    // Explicitly NOT overwriting: the destructive override is only ever
    // sent after the confirm below.
    expect(pushGitMirror).toHaveBeenCalledWith("tok-123", false);
    await screen.findByText(/gitMirror\.pushOk/);
  });

  // "Already up to date" is a success. Reporting it as one keeps a quiet
  // mirror from looking broken.
  it("reports an up-to-date remote as success, not a no-op warning", async () => {
    getGitMirror.mockResolvedValue(configured);
    pushGitMirror.mockResolvedValue({ pushed: 2, deleted: 0, changed: false, commit: "abc" });
    await renderPanel([sshCred]);

    await userEvent.click(screen.getByRole("button", { name: /gitMirror\.pushNow/ }));
    await screen.findByText("gitMirror.pushUpToDate");
  });

  it("cannot push before the mirror has been saved", async () => {
    await renderPanel([sshCred]);
    expect(screen.getByRole("button", { name: /gitMirror\.pushNow/ })).toBeDisabled();
  });

  // A failing push must surface the git error verbatim — "permission denied
  // (publickey)" names the fix and no paraphrase of ours would.
  it("shows the git failure from a manual push", async () => {
    getGitMirror.mockResolvedValue(configured);
    pushGitMirror.mockRejectedValue(new Error("permission denied (publickey)"));
    await renderPanel([sshCred]);

    await userEvent.click(screen.getByRole("button", { name: /gitMirror\.pushNow/ }));
    await screen.findByText("permission denied (publickey)");
  });

  // The stored status is what tells an admin they have been unmirrored for
  // three weeks rather than three minutes, so a failing row must render both
  // the error and the last time it worked.
  it("renders a failing status with the last successful push", async () => {
    getGitMirror.mockResolvedValue({
      ...configured,
      last_attempt_at: "2026-08-21T12:00:00Z",
      last_success_at: "2026-08-01T09:00:00Z",
      last_error: "host key mismatch for git.internal",
    });
    await renderPanel([sshCred]);
    await screen.findByText("gitMirror.statusFailing");
    expect(screen.getByText("host key mismatch for git.internal")).toBeInTheDocument();
    expect(screen.getByText(/gitMirror\.lastSuccess/)).toBeInTheDocument();
  });

  it("keeps the switch in sync with the server when enabling fails", async () => {
    getGitMirror.mockResolvedValue({ ...configured, enabled: false });
    putGitMirror.mockRejectedValue(new Error("nope"));
    await renderPanel([sshCred]);

    const toggle = screen.getByRole("switch");
    await userEvent.click(toggle);
    await screen.findByText("nope");
    // The optimistic flip must be rolled back: a switch that shows "on"
    // while the server holds "off" is worse than the error itself.
    await waitFor(() => expect(toggle).not.toBeChecked());
  });

  // The remote holds an unrelated repository. This must NOT read as a plain
  // error: overwriting it is destructive but sometimes correct, so it has to
  // arrive as a confirm whose text says what is about to be replaced.
  it("asks before overwriting a remote that shares no history", async () => {
    getGitMirror.mockResolvedValue(configured);
    pushGitMirror.mockRejectedValueOnce(
      new MockAPIError(409, "the remote holds a repository that shares no history with this workspace (2 ref(s) on the remote, none of them known here)"),
    );
    await renderPanel([sshCred]);

    await userEvent.click(screen.getByRole("button", { name: /gitMirror\.pushNow/ }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("gitMirror.unrelatedTitle");
    // The server's own detail is shown, not just our generic warning.
    expect(dialog).toHaveTextContent("2 ref(s) on the remote");

    pushGitMirror.mockResolvedValueOnce({
      pushed: 1,
      deleted: 2,
      changed: true,
      commit: "abcdef1234567890",
    });
    await userEvent.click(
      within(dialog).getByRole("button", { name: "gitMirror.unrelatedConfirm" }),
    );
    // Only the confirmed retry carries the override.
    expect(pushGitMirror).toHaveBeenNthCalledWith(1, "tok-123", false);
    expect(pushGitMirror).toHaveBeenNthCalledWith(2, "tok-123", true);
    await screen.findByText(/gitMirror\.pushOk/);
  });

  it("sends nothing when the overwrite confirm is cancelled", async () => {
    getGitMirror.mockResolvedValue(configured);
    pushGitMirror.mockRejectedValueOnce(new MockAPIError(409, "no shared history"));
    await renderPanel([sshCred]);

    await userEvent.click(screen.getByRole("button", { name: /gitMirror\.pushNow/ }));
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "common.cancel" }));
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
    // Exactly the one refused attempt — no overwrite slipped through.
    expect(pushGitMirror).toHaveBeenCalledTimes(1);
  });

  it("removes the mirror after confirmation", async () => {
    getGitMirror.mockResolvedValue(configured);
    await renderPanel([sshCred]);

    await userEvent.click(screen.getByRole("button", { name: /gitMirror\.remove/ }));
    // Confirmed through the shared ConfirmModal, not window.confirm — so
    // scope the second click to the dialog, where the confirm button shares
    // the trigger's label.
    const dialog = await screen.findByRole("alertdialog");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "gitMirror.remove" }),
    );
    expect(deleteGitMirror).toHaveBeenCalledWith("tok-123");
  });
});
