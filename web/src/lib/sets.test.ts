// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { toggleInSet } from "./sets";

describe("toggleInSet", () => {
  it("adds a missing item and removes a present one", () => {
    expect([...toggleInSet(new Set(["a"]), "b")]).toEqual(["a", "b"]);
    expect([...toggleInSet(new Set(["a", "b"]), "a")]).toEqual(["b"]);
  });

  it("does not mutate the input", () => {
    const s = new Set(["a"]);
    const next = toggleInSet(s, "a");
    expect(next).not.toBe(s);
    expect([...s]).toEqual(["a"]);
  });
});
