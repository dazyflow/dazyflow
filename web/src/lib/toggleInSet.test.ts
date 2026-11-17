// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { toggleInSet } from "./toggleInSet";

describe("toggleInSet", () => {
  it("adds a missing value and removes a present one without mutating", () => {
    const s = new Set(["a"]);
    expect([...toggleInSet(s, "b")]).toEqual(["a", "b"]);
    expect([...toggleInSet(s, "a")]).toEqual([]);
    expect([...s]).toEqual(["a"]);
  });
});
