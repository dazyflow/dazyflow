import { describe, expect, it } from "vitest";
import { pickActive } from "./pickActive";
describe("pickActive", () => {
    it("returns the cached value when it's still available", () => {
        // The most common case: user previously picked "ws-b"; on reload
        // we want their choice to stick.
        expect(pickActive(["ws-a", "ws-b", "ws-c"], "ws-b", "ws-a")).toBe("ws-b");
    });
    it("falls back to the principal's binding when cache is stale", () => {
        // The user previously selected "ws-old" in a different tenant.
        // After switching to a tenant where "ws-old" doesn't exist, we
        // shouldn't keep it — pick the principal's home workspace if it
        // happens to be in the new list.
        expect(pickActive(["main", "secondary"], "ws-old", "main")).toBe("main");
    });
    it("falls back to the first entry when neither cache nor binding match", () => {
        // First-load case for a tenant admin with no binding: just pick
        // something so the UI isn't blank.
        expect(pickActive(["alpha", "beta", "gamma"], "", "")).toBe("alpha");
    });
    it("returns empty when the available list is empty", () => {
        // Switcher placeholder state — no API calls should fire.
        expect(pickActive([], "anything", "anything")).toBe("");
    });
    it("ignores a cache value that isn't in the available list", () => {
        // The cache is from the previous tenant where this workspace
        // existed; in the new tenant it doesn't.
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
        // The user's explicit selection wins — their binding is only the
        // initial-state hint, not a forced choice.
        expect(pickActive(["a", "b"], "b", "a")).toBe("b");
    });
    it("treats empty strings as 'not set', not as a real selection", () => {
        // Empty cached/bound strings should fall through, not match the
        // empty option (which doesn't exist in the list anyway).
        expect(pickActive(["only"], "", "")).toBe("only");
    });
    it("is a pure function (no side effects on inputs)", () => {
        // The available list shouldn't be mutated — important since the
        // caller may reuse it for the switcher dropdown.
        const list = ["a", "b", "c"];
        const before = list.slice();
        pickActive(list, "b", "a");
        expect(list).toEqual(before);
    });
});
