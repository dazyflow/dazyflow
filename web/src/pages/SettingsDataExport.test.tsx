// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The account's own data export — GDPR Art. 15/20. The daemon has assembled
// the document for a while; the thing under test is that a person can actually
// get at it, and that a refusal is visible instead of landing in a saved file.
import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("react-i18next", () => {
  const t = (k: string) => k;
  const value = { t, i18n: { language: "en", changeLanguage: () => {} } };
  return { useTranslation: () => value };
});
vi.mock("../auth", () => {
  const auth = { token: "session_tok" };
  return { useAuth: () => auth };
});

const exportMyData = vi.fn();
const getPreferences = vi.fn();
const totpStatus = vi.fn();
vi.mock("../api", () => ({
  APIError: class extends Error {
    status: number;
    constructor(status: number, message: string) {
      super(message);
      this.status = status;
    }
  },
  api: {
    exportMyData: (...a: unknown[]) => exportMyData(...a),
    getPreferences: (...a: unknown[]) => getPreferences(...a),
    totpStatus: (...a: unknown[]) => totpStatus(...a),
  },
}));

const downloadJson = vi.fn();
vi.mock("../lib/download", () => ({
  downloadJson: (...a: unknown[]) => downloadJson(...a),
  downloadText: () => {},
}));

import { Settings } from "./Settings";

const quietStores = () => {
  getPreferences.mockResolvedValue({
    email_on_flow_failure: false,
    email_on_support_reply: false,
  });
  totpStatus.mockResolvedValue({ enabled: false });
};

describe("Settings — your data", () => {
  it("saves the export the endpoint returns", async () => {
    quietStores();
    exportMyData.mockResolvedValue({ profile: { email: "ada@example.com" } });
    render(<Settings />);

    await userEvent.click(screen.getByRole("button", { name: /dataExport.download/ }));

    await waitFor(() => expect(downloadJson).toHaveBeenCalledTimes(1));
    expect(exportMyData).toHaveBeenCalledWith("session_tok");
    const [data, filename] = downloadJson.mock.calls[0];
    expect(data).toEqual({ profile: { email: "ada@example.com" } });
    // Dated: a folder of files all called dazyflow-my-data.json says nothing
    // about which one is current.
    expect(filename).toMatch(/^dazyflow-my-data-\d{4}-\d{2}-\d{2}\.json$/);
  });

  it("confirms the save so the click isn't silent", async () => {
    quietStores();
    exportMyData.mockResolvedValue({});
    render(<Settings />);
    await userEvent.click(screen.getByRole("button", { name: /dataExport.download/ }));
    expect(await screen.findByText("dataExport.saved")).toBeInTheDocument();
  });

  it("shows a refusal in the card instead of saving a file", async () => {
    // A 403 here is the daemon's session_required rule. Whatever the reason, a
    // downloaded file containing an error message would be the worst outcome.
    quietStores();
    exportMyData.mockRejectedValue(new Error("nope"));
    render(<Settings />);

    await userEvent.click(screen.getByRole("button", { name: /dataExport.download/ }));

    // explainApiError maps an unrecognised failure to the generic key.
    await waitFor(() => expect(screen.getByText("apiError.generic")).toBeInTheDocument());
    expect(downloadJson).not.toHaveBeenCalled();
  });

  it("says what the file contains before asking for a click", async () => {
    // The list is the informed half of "download a copy of your data": what is
    // in it, and — for the API keys — what deliberately isn't.
    quietStores();
    render(<Settings />);
    for (const key of [
      "dataExport.itemProfile",
      "dataExport.itemMemberships",
      "dataExport.itemKeys",
      "dataExport.itemFlows",
      "dataExport.orgNote",
    ]) {
      expect(screen.getByText(key)).toBeInTheDocument();
    }
  });
});
