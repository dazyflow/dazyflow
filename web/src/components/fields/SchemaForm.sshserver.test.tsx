// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));
vi.mock("../../i18n", () => ({ default: { t: (k: string) => k } }));
vi.mock("../../auth", () => ({
  useAuth: () => ({ token: "tok", hasPerm: () => true }),
}));

import { SchemaForm } from "./SchemaForm";
import { api } from "../../api";
import type { JSONSchema } from "../../types";

function schema(extra: Partial<JSONSchema>): JSONSchema {
  return {
    type: "object",
    properties: {
      account: { type: "string", title: "Server", format: "ssh-account", ...extra },
    },
  } as JSONSchema;
}

function renderField(s: JSONSchema, value: Record<string, unknown> = {}) {
  return render(
    <MemoryRouter>
      <SchemaForm schema={s} value={value} onChange={() => {}} />
    </MemoryRouter>,
  );
}

// A server the org has not saved must never look picked. The step reports
// "no saved server called X" at run time, and a dropdown that quietly named one
// is how a flow ends up pointed at a machine that does not exist.
describe("the saved-server picker", () => {
  it("holds nothing when there is no server to hold", async () => {
    vi.spyOn(api, "listSSHCredentials").mockResolvedValue({ credentials: [] });
    renderField(schema({}));

    const select = await screen.findByRole("combobox");
    await waitFor(() =>
      expect(screen.getByRole("option", { name: "sshCreds.noneYet" })).toBeTruthy(),
    );
    expect((select as HTMLSelectElement).value).toBe("");
    expect(screen.getAllByRole("option")).toHaveLength(1);
  });

  it("ignores a schema default, which names no saved server", async () => {
    vi.spyOn(api, "listSSHCredentials").mockResolvedValue({
      credentials: [{ account: "web-1", host: "h", has_password: true } as never],
    });
    renderField(schema({ default: "default" }));

    const select = await screen.findByRole("combobox");
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(2));
    expect((select as HTMLSelectElement).value).toBe("");
    expect(screen.queryByRole("option", { name: "default" })).toBeNull();
  });

  it("offers the integration's own connection where blank means that", async () => {
    vi.spyOn(api, "listSSHCredentials").mockResolvedValue({ credentials: [] });
    renderField(schema({ x_blank_connection: true }));

    await waitFor(() =>
      expect(screen.getByRole("option", { name: "sshCreds.useConnection" })).toBeTruthy(),
    );
  });

  it("keeps a chosen server that has since been deleted", async () => {
    vi.spyOn(api, "listSSHCredentials").mockResolvedValue({
      credentials: [{ account: "web-1", host: "h", has_password: true } as never],
    });
    renderField(schema({}), { account: "gone" });

    const select = await screen.findByRole("combobox");
    await waitFor(() => expect((select as HTMLSelectElement).value).toBe("gone"));
  });
});
