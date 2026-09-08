// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { pickActive } from "./pickActive";

describe("pickActive", () => {
  it("returns the cached value when it's still available", () => {
    expect(pickActive(["ws-a", "ws-b", "ws-c"], "ws-b", "ws-a")).toBe(
      "ws-b",
    );
  });

  it("falls back to the principal's binding when cache is stale", () => {
    expect(pickActive(["main", "secondary"], "ws-old", "main")).toBe("main");
  });

  it("falls back to the first entry when neither cache nor binding match", () => {
    expect(pickActive(["alpha", "beta", "gamma"], "", "")).toBe("alpha");
  });

  it("returns empty when the available list is empty", () => {
    expect(pickActive([], "anything", "anything")).toBe("");
  });

  it("ignores a cache value that isn't in the available list", () => {
    expect(pickActive(["new-a", "new-b"], "leftover", "")).toBe("new-a");
  });

  it("ignores an out-of-list binding too", () => {
    // me.workspace = "" is the common admin case, but a binding from
    // a different tenant could leak in if the principal moved.
    // Confirm we fall through to the first entry rather than echoing
    // the bogus binding.
    expect(pickActive(["new-a"], "", "from-other-tenant")).toBe("new-a");
  });

  it("prefers cache over binding when both are present", () => {
    expect(pickActive(["a", "b"], "b", "a")).toBe("b");
  });

  it("treats empty strings as 'not set', not as a real selection", () => {
    // Empty cached/bound strings should fall through, not match the
    // empty option (which doesn't exist in the list anyway).
    expect(pickActive(["only"], "", "")).toBe("only");
  });

  it("is a pure function (no side effects on inputs)", () => {
    const list = ["a", "b", "c"];
    const before = list.slice();
    pickActive(list, "b", "a");
    expect(list).toEqual(before);
  });
});
