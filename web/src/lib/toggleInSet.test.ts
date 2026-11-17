import { describe, expect, it } from "vitest";
import { toggleInSet } from "./toggleInSet";

describe("toggleInSet", () => {
  it("adds a missing item", () => {
    expect([...toggleInSet(new Set(["a"]), "b")]).toEqual(["a", "b"]);
  });

  it("removes a present item", () => {
    expect([...toggleInSet(new Set(["a", "b"]), "a")]).toEqual(["b"]);
  });

  it("does not mutate the input", () => {
    const s = new Set(["a"]);
    const next = toggleInSet(s, "a");
    expect(next).not.toBe(s);
    expect(s.has("a")).toBe(true);
  });
});
