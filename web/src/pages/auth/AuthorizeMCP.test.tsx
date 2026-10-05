// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The OAuth consent page an MCP client (claude.ai, the Claude apps) sends the
// user to. Under test: it says who is asking, and approving or denying hands
// the browser to exactly the URL the server returned.
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  const value = { t, i18n: { language: "en", changeLanguage: () => {} } };
  return {
    useTranslation: () => value,
    Trans: ({ i18nKey, values }: { i18nKey: string; values?: Record<string, string> }) =>
      `${i18nKey} ${Object.values(values ?? {}).join(" ")}`,
    initReactI18next: { type: "3rdParty", init: () => {} },
  };
});
vi.mock("../../auth", () => {
  const auth = { token: "session_tok" };
  return { useAuth: () => auth };
});

const getMCPAuthorization = vi.fn();
const decideMCPAuthorization = vi.fn();
vi.mock("../../api", () => ({
  api: {
    getMCPAuthorization: (...a: unknown[]) => getMCPAuthorization(...a),
    decideMCPAuthorization: (...a: unknown[]) => decideMCPAuthorization(...a),
  },
}));

import { AuthorizeMCP } from "./AuthorizeMCP";

const assign = vi.fn();
Object.defineProperty(window, "location", { value: { ...window.location, assign }, writable: true });

afterEach(() => vi.clearAllMocks());

const renderAt = (url: string) =>
  render(
    <MemoryRouter initialEntries={[url]}>
      <AuthorizeMCP />
    </MemoryRouter>,
  );

describe("AuthorizeMCP", () => {
  it("shows who is asking and follows the approval redirect", async () => {
    getMCPAuthorization.mockResolvedValue({
      client_name: "Claude",
      redirect_host: "claude.ai",
      scope: "mcp",
      account: "ada@example.com",
      workspace: "t/ws",
    });
    decideMCPAuthorization.mockResolvedValue({ redirect_url: "https://claude.ai/cb?code=c&state=s" });
    renderAt("/authorize?request=r1");

    expect(await screen.findByText(/Claude ada@example.com t\/ws/)).toBeTruthy();
    expect(screen.getByText(/claude\.ai/)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "authorizeMcp.approve" }));

    await waitFor(() => expect(assign).toHaveBeenCalledWith("https://claude.ai/cb?code=c&state=s"));
    expect(getMCPAuthorization).toHaveBeenCalledWith("session_tok", "r1");
    expect(decideMCPAuthorization).toHaveBeenCalledWith("session_tok", "r1", true);
  });

  it("denies", async () => {
    getMCPAuthorization.mockResolvedValue({
      client_name: "Claude", redirect_host: "claude.ai", scope: "mcp", account: "a", workspace: "t/ws",
    });
    decideMCPAuthorization.mockResolvedValue({ redirect_url: "https://claude.ai/cb?error=access_denied" });
    renderAt("/authorize?request=r1");

    await userEvent.click(await screen.findByRole("button", { name: "authorizeMcp.deny" }));
    await waitFor(() => expect(decideMCPAuthorization).toHaveBeenCalledWith("session_tok", "r1", false));
  });
});
