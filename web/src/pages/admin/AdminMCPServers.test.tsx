// SPDX-FileCopyrightText: 2026 Joachim Klahr
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
  return {
    useTranslation: () => value,
    Trans: ({ i18nKey }: { i18nKey: string }) => <>{i18nKey}</>,
  };
});
vi.mock("../../auth", () => {
  const auth = { token: "tok", me: { tenant: "t", workspace: "ws" }, hasPerm: () => true };
  return { useAuth: () => auth };
});

const listMCPServers = vi.fn();
const saveMCPServer = vi.fn();
const refreshMCPServer = vi.fn();
const deleteMCPServer = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  api: {
    listMCPServers: (...a: unknown[]) => listMCPServers(...a),
    saveMCPServer: (...a: unknown[]) => saveMCPServer(...a),
    refreshMCPServer: (...a: unknown[]) => refreshMCPServer(...a),
    deleteMCPServer: (...a: unknown[]) => deleteMCPServer(...a),
  },
}));

import { AdminMCPServers } from "./AdminMCPServers";

const connected = {
  name: "vendor",
  url: "https://vendor.test/mcp",
  auth_kind: "bearer" as const,
  has_token: true,
  enabled: true,
  connected: true,
  tool_ids: ["mcp:vendor:search", "mcp:vendor:create", "mcp:vendor:list", "mcp:vendor:delete"],
  tool_count: 4,
  created_at: "2026-08-01T10:00:00Z",
  updated_at: "2026-08-01T10:00:00Z",
};

beforeEach(() => {
  vi.clearAllMocks();
  listMCPServers.mockResolvedValue({ servers: [connected] });
});

describe("AdminMCPServers", () => {
  it("names the steps the server contributed, not just how many", async () => {
    render(<AdminMCPServers />);
    // A count alone leaves an admin guessing what to search the palette for.
    await screen.findByText(/search, create, list/);
    expect(screen.getByText(/mcp.andMore/)).toBeInTheDocument();
  });

  it("does not ask for the token again when one is already stored", async () => {
    render(<AdminMCPServers />);
    await screen.findByText("vendor");
    await userEvent.click(screen.getByLabelText("common.edit"));

    const token = await screen.findByLabelText("mcp.tokenLabel");
    // Empty, not required, and it says why — the stored credential is kept.
    expect(token).toHaveValue("");
    expect(token).not.toBeRequired();
    expect(token).toHaveAttribute("placeholder", "mcp.tokenKeepPlaceholder");
  });

  it("locks the name when editing, because flows reference it", async () => {
    render(<AdminMCPServers />);
    await screen.findByText("vendor");
    await userEvent.click(screen.getByLabelText("common.edit"));

    expect(await screen.findByLabelText("mcp.nameLabel")).toBeDisabled();
    expect(screen.getByText("mcp.nameLockedHint")).toBeInTheDocument();
  });

  it("sends no token field when the box was left blank, so the stored one is kept", async () => {
    saveMCPServer.mockResolvedValue({ ...connected, url: "https://moved.test/mcp" });
    render(<AdminMCPServers />);
    await screen.findByText("vendor");
    await userEvent.click(screen.getByLabelText("common.edit"));

    const url = await screen.findByLabelText("mcp.urlLabel");
    await userEvent.clear(url);
    await userEvent.type(url, "https://moved.test/mcp");
    await userEvent.click(screen.getByText("mcp.saveAndConnect"));

    await waitFor(() => expect(saveMCPServer).toHaveBeenCalled());
    const [, input, existingName] = saveMCPServer.mock.calls[0];
    expect(existingName).toBe("vendor");
    expect(input.token).toBeUndefined();
    expect(input.url).toBe("https://moved.test/mcp");
  });

  it("reports a server that saved but would not connect", async () => {
    // Saving succeeds and the row carries the reason — the page has to say so,
    // or someone walks away from a server that will never work.
    saveMCPServer.mockResolvedValue({
      ...connected,
      connected: false,
      last_error: "mcp server refused the credential (HTTP 401)",
    });
    render(<AdminMCPServers />);
    await screen.findByText("vendor");
    await userEvent.click(screen.getByLabelText("common.edit"));
    await userEvent.click(screen.getByText("mcp.saveAndConnect"));

    expect(await screen.findByText(/mcp.savedButFailed/)).toBeInTheDocument();
  });

  it("confirms before removing, because flows stop running", async () => {
    deleteMCPServer.mockResolvedValue({ deleted: "vendor" });
    render(<AdminMCPServers />);
    await screen.findByText("vendor");
    await userEvent.click(screen.getByLabelText("common.remove"));

    expect(await screen.findByText(/mcp.removeReally/)).toBeInTheDocument();
    expect(deleteMCPServer).not.toHaveBeenCalled();

    await userEvent.click(screen.getByText("common.remove"));
    await waitFor(() => expect(deleteMCPServer).toHaveBeenCalledWith("tok", "vendor"));
  });

  it("shows a paused server as off rather than as broken", async () => {
    listMCPServers.mockResolvedValue({
      servers: [{ ...connected, enabled: false, connected: false, tool_ids: [] }],
    });
    render(<AdminMCPServers />);
    expect(await screen.findByText("mcp.disabled")).toBeInTheDocument();
    expect(screen.queryByText("mcp.failing")).not.toBeInTheDocument();
  });

  it("offers the add form with no server preloaded", async () => {
    saveMCPServer.mockResolvedValue(connected);
    render(<AdminMCPServers />);
    await screen.findByText("vendor");
    await userEvent.click(screen.getByText("mcp.add"));

    const name = await screen.findByLabelText("mcp.nameLabel");
    expect(name).toBeEnabled();
    expect(name).toHaveValue("");
  });
});
