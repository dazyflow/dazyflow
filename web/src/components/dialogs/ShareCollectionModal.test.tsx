// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The share dialog for a collection.
//
// The requirement carrying the weight is the warning: this link publishes the
// collection's rows to anyone who ends up holding the URL, and the person
// clicking may not have thought about what is in there. So the consequence
// has to be on screen BEFORE the link exists — not after, and not only in the
// docs.

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("react-i18next", () => {
  const t = (k: string, o?: Record<string, unknown>) =>
    o && typeof o === "object" ? `${k}:${Object.values(o).join(",")}` : k;
  return { useTranslation: () => ({ t }) };
});
vi.mock("../../auth", () => ({
  useAuth: () => ({ token: "tok", activeTenant: "acme", activeWorkspace: "main" }),
}));

const getCollectionShare = vi.fn();
const createCollectionShare = vi.fn();
const deleteCollectionShare = vi.fn();
vi.mock("../../api", () => ({
  APIError: class extends Error {},
  isErrorCode: (e: unknown, code: string) =>
    !!e && typeof e === "object" && (e as { code?: string }).code === code,
  api: {
    getCollectionShare: (...a: unknown[]) => getCollectionShare(...a),
    createCollectionShare: (...a: unknown[]) => createCollectionShare(...a),
    deleteCollectionShare: (...a: unknown[]) => deleteCollectionShare(...a),
  },
}));

function apiError(code: string): Error & { code: string } {
  return Object.assign(new Error(code), { code });
}

import { ShareCollectionModal } from "./ShareCollectionModal";

const link = {
  collection: "leads",
  token: "tok-abc",
  url: "http://internal:8642/board/tok-abc",
  created_at: "2026-09-03T10:00:00Z",
};

beforeEach(() => {
  getCollectionShare.mockReset();
  createCollectionShare.mockReset();
  deleteCollectionShare.mockReset();
});

const openDialog = (onChange = vi.fn()) =>
  render(
    <ShareCollectionModal
      collection="leads"
      onClose={vi.fn()}
      onChange={onChange}
    />,
  );

describe("ShareCollectionModal", () => {
  it("warns what publishing means before any link exists", async () => {
    getCollectionShare.mockRejectedValue(apiError("share_not_found"));
    openDialog();

    await waitFor(() =>
      expect(
        screen.getByText("shareCollection.beforeYouShare"),
      ).toBeInTheDocument(),
    );
    // The create button is there, but the warning came first.
    expect(
      screen.getByRole("button", { name: /shareCollection\.create/ }),
    ).toBeInTheDocument();
    // And no URL is on screen yet.
    expect(document.querySelector(".secret-reveal")).toBeNull();
  });

  it("treats no-link-yet as the normal first-open state, not an error", async () => {
    getCollectionShare.mockRejectedValue(apiError("share_not_found"));
    openDialog();
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: /shareCollection\.create/ }),
      ).toBeInTheDocument(),
    );
    expect(screen.queryByText(/apiError|internal_error/)).toBeNull();
  });

  // Behind a dev proxy the daemon's derived base URL can be the internal
  // host, while window.location.origin is always the address the operator is
  // actually looking at.
  it("shows the link against the browser's own origin", async () => {
    getCollectionShare.mockResolvedValue(link);
    openDialog();
    await waitFor(() =>
      expect(document.querySelector(".secret-reveal")?.textContent).toBe(
        `${window.location.origin}/board/tok-abc`,
      ),
    );
    // Not the server-reported internal host.
    expect(document.body.textContent).not.toContain("internal:8642");
  });

  it("mints a link and tells the caller", async () => {
    getCollectionShare.mockRejectedValue(apiError("share_not_found"));
    createCollectionShare.mockResolvedValue(link);
    const onChange = vi.fn();
    openDialog(onChange);

    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: /shareCollection\.create/ }),
      ).toBeInTheDocument(),
    );
    await userEvent.click(
      screen.getByRole("button", { name: /shareCollection\.create/ }),
    );

    await waitFor(() =>
      expect(createCollectionShare).toHaveBeenCalledWith(
        "tok",
        "leads",
        "acme",
        "main",
      ),
    );
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(link));
    // Once live, the dialog says the link keeps following the collection.
    expect(screen.getByText("shareCollection.liveWarning")).toBeInTheDocument();
  });

  it("revokes a link and tells the caller", async () => {
    getCollectionShare.mockResolvedValue(link);
    deleteCollectionShare.mockResolvedValue(undefined);
    const onChange = vi.fn();
    openDialog(onChange);

    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: /shareCollection\.disable/ }),
      ).toBeInTheDocument(),
    );
    await userEvent.click(
      screen.getByRole("button", { name: /shareCollection\.disable/ }),
    );

    await waitFor(() =>
      expect(deleteCollectionShare).toHaveBeenCalledWith(
        "tok",
        "leads",
        "acme",
        "main",
      ),
    );
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(null));
    // Back to the pre-publication state, warning included.
    expect(
      screen.getByText("shareCollection.beforeYouShare"),
    ).toBeInTheDocument();
  });

  // A viewer who lacks edit authority gets the reason, not a raw 403.
  it("explains a refusal in words", async () => {
    getCollectionShare.mockRejectedValue(apiError("share_not_found"));
    createCollectionShare.mockRejectedValue(apiError("forbidden"));
    openDialog();

    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: /shareCollection\.create/ }),
      ).toBeInTheDocument(),
    );
    await userEvent.click(
      screen.getByRole("button", { name: /shareCollection\.create/ }),
    );
    await waitFor(() =>
      expect(screen.getByText("shareCollection.forbidden")).toBeInTheDocument(),
    );
  });
});
