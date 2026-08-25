// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { POLL } from "../../lib/timing";
import userEvent from "@testing-library/user-event";

// Stable `t` and auth: the page's load callback lists `t` in its deps, so a
// fresh function per render would re-fire it forever.
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
const mintRunnerToken = vi.fn();
const deleteRunner = vi.fn();
const setRunnerLabels = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    listRunners: (...a: unknown[]) => listRunners(...a),
    mintRunnerToken: (...a: unknown[]) => mintRunnerToken(...a),
    deleteRunner: (...a: unknown[]) => deleteRunner(...a),
    setRunnerLabels: (...a: unknown[]) => setRunnerLabels(...a),
  },
}));

import { AdminRunners } from "./AdminRunners";

// Setting up a runner is: press a button, copy one line, paste it elsewhere.
// The page's whole job is to make that line available exactly once and then be
// honest about which machines have actually turned up.

const online = {
  name: "invoices-box",
  labels: ["linux", "x64"],
  version: "0.1.0",
  online: true,
  last_seen: "2026-08-25T09:00:00Z",
  created_at: "2026-08-01T10:00:00Z",
};

const offline = {
  name: "old-laptop",
  labels: ["linux"],
  version: "0.1.0",
  online: false,
  last_seen: "2026-08-20T09:00:00Z",
  created_at: "2026-08-01T10:00:00Z",
};

beforeEach(() => {
  listRunners.mockResolvedValue({ runners: [] });
  mintRunnerToken.mockResolvedValue({
    token: "dzrt_abc123",
    expires_at: "2026-08-25T10:00:00Z",
  });
  deleteRunner.mockResolvedValue({});
  setRunnerLabels.mockImplementation((_tok: string, name: string, labels: string[]) =>
    // The server normalizes and returns the saved row; the page shows what came
    // back rather than what was typed.
    Promise.resolve({ ...online, name, labels: [...labels].map((l) => l.trim().toLowerCase()).sort() }),
  );
});

afterEach(() => {
  vi.useRealTimers();
});

describe("AdminRunners", () => {
  it("offers a way in when nothing is registered", async () => {
    render(<AdminRunners />);
    expect(await screen.findByText("runners.emptyTitle")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "runners.add" })).toBeEnabled();
  });

  // The command is the whole setup, so it has to carry the token and the
  // server's own address — the operator should not have to supply either.
  it("shows a copyable one-line install command", async () => {
    const user = userEvent.setup();
    render(<AdminRunners />);
    await screen.findByText("runners.emptyTitle");

    await user.click(screen.getByRole("button", { name: "runners.add" }));
    await waitFor(() => expect(mintRunnerToken).toHaveBeenCalledWith("tok"));

    const cmd = await screen.findByText(/runner\.sh/);
    expect(cmd.textContent).toContain("dzrt_abc123");
    // Served by this very daemon, so the address is already known.
    expect(cmd.textContent).toContain(window.location.origin);
    // --service is part of the command, not an option to discover. A runner
    // that dies with the terminal fails silently — the machine just stops
    // appearing days later — so the default has to be the one that survives a
    // reboot.
    expect(cmd.textContent).toContain("--service");

    // Read it back through userEvent's own clipboard rather than spying on
    // writeText: userEvent installs its own stub, so a hand-rolled spy is
    // replaced and never called. Reading back also tests the thing that
    // matters — what would land in the operator's paste buffer.
    await user.click(screen.getByRole("button", { name: /common.copy/ }));
    await waitFor(async () => {
      expect(await navigator.clipboard.readText()).toContain("dzrt_abc123");
    });
    // And the button says so, which is the only feedback there is.
    expect(await screen.findByText("common.copied")).toBeInTheDocument();
  });

  // The token is returned once and cannot be fetched again, so the page has to
  // say when it stops working rather than leaving someone to discover it.
  it("says when the command expires", async () => {
    const user = userEvent.setup();
    render(<AdminRunners />);
    await screen.findByText("runners.emptyTitle");
    await user.click(screen.getByRole("button", { name: "runners.add" }));
    expect(await screen.findByText(/runners.installExpiry/)).toBeInTheDocument();
  });

  it("keeps the command until it is dismissed", async () => {
    const user = userEvent.setup();
    render(<AdminRunners />);
    await screen.findByText("runners.emptyTitle");
    await user.click(screen.getByRole("button", { name: "runners.add" }));
    await screen.findByText(/runner\.sh/);

    await user.click(screen.getByRole("button", { name: "common.close" }));
    expect(screen.queryByText(/runner\.sh/)).not.toBeInTheDocument();
  });

  it("lists a machine that has checked in", async () => {
    listRunners.mockResolvedValue({ runners: [online] });
    render(<AdminRunners />);
    expect(await screen.findByText("invoices-box")).toBeInTheDocument();
    expect(screen.getByText("runners.online")).toBeInTheDocument();
    expect(screen.getByText(/linux · x64/)).toBeInTheDocument();
    // The agent version, because an old agent is a plausible cause of odd
    // behaviour.
    expect(screen.getByText("0.1.0")).toBeInTheDocument();
  });

  // "Offline since Tuesday" is the whole story of what went wrong, so an
  // offline machine shows when it was last seen and an online one does not
  // need to.
  it("says how long a machine has been gone", async () => {
    listRunners.mockResolvedValue({ runners: [offline] });
    render(<AdminRunners />);
    expect(await screen.findByText(/runners.offlineSince/)).toBeInTheDocument();
    expect(screen.queryByText("runners.online")).not.toBeInTheDocument();
  });

  it("distinguishes a machine that never connected", async () => {
    listRunners.mockResolvedValue({
      runners: [{ ...offline, last_seen: undefined }],
    });
    render(<AdminRunners />);
    // A machine that never arrived is a different problem from one that went
    // away: the token was probably never pasted.
    expect(await screen.findByText("runners.neverSeen")).toBeInTheDocument();
  });

  it("removes a machine, once it has been confirmed", async () => {
    const user = userEvent.setup();
    listRunners.mockResolvedValue({ runners: [online] });
    render(<AdminRunners />);
    await screen.findByText("invoices-box");

    // Removing a runner revokes its credential and cannot be undone — getting
    // the machine back means a fresh token and a second visit to it. So the
    // first click asks rather than acting, like every other admin page.
    await user.click(screen.getByRole("button", { name: "runners.remove" }));
    expect(deleteRunner).not.toHaveBeenCalled();
    expect(screen.getByText("runners.removeReally")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "common.remove" }));
    await waitFor(() => expect(deleteRunner).toHaveBeenCalledWith("tok", "invoices-box"));
  });

  it("lets the operator back out of removing a machine", async () => {
    const user = userEvent.setup();
    listRunners.mockResolvedValue({ runners: [online] });
    render(<AdminRunners />);
    await screen.findByText("invoices-box");

    await user.click(screen.getByRole("button", { name: "runners.remove" }));
    await user.click(screen.getByRole("button", { name: "common.cancel" }));
    expect(deleteRunner).not.toHaveBeenCalled();
    expect(screen.queryByText("runners.removeReally")).not.toBeInTheDocument();
  });

  // The page warns on every visit, not only when a runner exists. Whoever can
  // edit a flow can run commands on these machines, and that is worth knowing
  // before the first one is added.
  it("always states what a runner can be told to do", async () => {
    render(<AdminRunners />);
    await screen.findByText("runners.emptyTitle");
    expect(screen.getByText(/runners.securityNote/)).toBeInTheDocument();
  });

  it("surfaces a failure to load", async () => {
    listRunners.mockRejectedValue(new Error("nope"));
    render(<AdminRunners />);
    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });

  // ---- assigning labels ---------------------------------------------

  // A label used to be decided on the machine at install time and fixed there
  // forever: putting an existing server into another pool meant a visit to it,
  // or deleting the runner and re-installing with a fresh token.
  it("assigns a label to a registered machine", async () => {
    const user = userEvent.setup();
    listRunners.mockResolvedValue({ runners: [online] });
    render(<AdminRunners />);
    await screen.findByText("invoices-box");

    await user.click(screen.getByRole("button", { name: "runners.editLabels" }));
    await user.type(screen.getByLabelText("runners.labelPlaceholder"), "Build ");
    await user.click(screen.getByRole("button", { name: /runners.labelAdd/ }));

    // The whole set goes up, not a diff: the set is what routes work, so two
    // admins editing one machine each end with a set they meant.
    await waitFor(() =>
      expect(setRunnerLabels).toHaveBeenCalledWith("tok", "invoices-box", ["linux", "x64", "Build"]),
    );
    // And the row shows the server's normalized answer, so it is visible that a
    // step has to spell it "build".
    expect(await screen.findByText(/build · linux · x64/)).toBeInTheDocument();
  });

  it("removes a label", async () => {
    const user = userEvent.setup();
    listRunners.mockResolvedValue({ runners: [online] });
    render(<AdminRunners />);
    await screen.findByText("invoices-box");

    await user.click(screen.getByRole("button", { name: "runners.editLabels" }));
    await user.click(screen.getByRole("button", { name: /runners.labelRemove.*x64/ }));
    await waitFor(() =>
      expect(setRunnerLabels).toHaveBeenCalledWith("tok", "invoices-box", ["linux"]),
    );
  });

  it("does not spend a request on a label the machine already carries", async () => {
    const user = userEvent.setup();
    listRunners.mockResolvedValue({ runners: [online] });
    render(<AdminRunners />);
    await screen.findByText("invoices-box");

    await user.click(screen.getByRole("button", { name: "runners.editLabels" }));
    await user.type(screen.getByLabelText("runners.labelPlaceholder"), "LINUX{Enter}");
    expect(setRunnerLabels).not.toHaveBeenCalled();
  });

  // A rejected label (a comma, a seventeenth pool) must not look as though it
  // stuck: the failure surfaces and the row keeps what the server still holds.
  it("says when a label was refused, and keeps the old set", async () => {
    const user = userEvent.setup();
    listRunners.mockResolvedValue({ runners: [online] });
    setRunnerLabels.mockRejectedValue(new Error("comma"));
    render(<AdminRunners />);
    await screen.findByText("invoices-box");

    await user.click(screen.getByRole("button", { name: "runners.editLabels" }));
    await user.type(screen.getByLabelText("runners.labelPlaceholder"), "a,b{Enter}");
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.getByText(/linux · x64/)).toBeInTheDocument();
  });

  // The editor is a row of its own under the machine, not a box inside the
  // labels cell — on a phone that column is narrower than the input needs.
  it("opens the editor in its own full-width row", async () => {
    const user = userEvent.setup();
    listRunners.mockResolvedValue({ runners: [online] });
    const { container } = render(<AdminRunners />);
    await screen.findByText("invoices-box");

    await user.click(screen.getByRole("button", { name: "runners.editLabels" }));
    const cell = container.querySelector(".runner-label-row td");
    expect(cell).toHaveAttribute("colspan", "5");

    await user.click(screen.getByRole("button", { name: "common.close" }));
    expect(container.querySelector(".runner-label-row")).toBeNull();
  });

  // The table keeps a readable minimum width on a narrow screen, so the columns
  // that stick out have to SCROLL. Without this wrapper the card's
  // overflow:hidden clipped them and the last columns were unreachable on a
  // phone — which is exactly what was reported.
  it("lets the table scroll sideways instead of clipping it", async () => {
    listRunners.mockResolvedValue({ runners: [online] });
    const { container } = render(<AdminRunners />);
    await screen.findByText("invoices-box");
    expect(container.querySelector(".run-table-scroll .run-table")).not.toBeNull();
  });

  // A machine appears seconds after the command is pasted, and that wait is
  // when someone is most likely to think it failed — so the list refreshes
  // itself rather than needing a reload.
  it("picks up a machine that arrives while the page is open", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(<AdminRunners />);
    await screen.findByText("runners.emptyTitle");

    listRunners.mockResolvedValue({ runners: [online] });
    // Inside act(): advancing the timer fires the poll, whose promise resolves
    // into setRunners after the await returns. Outside, React warns that the
    // update happened unobserved — and the warning is the honest one, since the
    // assertion below would then be racing the render rather than following it.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL.watched + 1000);
    });
    await waitFor(() => expect(screen.getByText("invoices-box")).toBeInTheDocument());
  });
});
