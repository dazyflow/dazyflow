import { describe, it, expect } from "vitest";
import { tokenizeValue, serializeEditable } from "./SchemaForm";

// chip builds a token chip span the way the editor's renderInto does: a
// contenteditable=false span whose data-token carries the raw ${…} and whose
// visible text is just a label (which serialize must ignore).
function chip(token: string, label: string): HTMLSpanElement {
  const s = document.createElement("span");
  s.setAttribute("data-token", token);
  s.setAttribute("contenteditable", "false");
  s.textContent = label;
  return s;
}

describe("tokenizeValue", () => {
  it("returns nothing for an empty string", () => {
    expect(tokenizeValue("")).toEqual([]);
  });

  it("treats plain text as a single text segment", () => {
    expect(tokenizeValue("hello world")).toEqual([
      { kind: "text", text: "hello world" },
    ]);
  });

  it("treats a whole-value token as one token segment", () => {
    expect(tokenizeValue("${secret.API_KEY}")).toEqual([
      { kind: "token", token: "${secret.API_KEY}" },
    ]);
  });

  it("splits mixed text + token (the Bearer case)", () => {
    expect(tokenizeValue("Bearer ${secret.API_KEY}")).toEqual([
      { kind: "text", text: "Bearer " },
      { kind: "token", token: "${secret.API_KEY}" },
    ]);
  });

  it("handles multiple tokens with text around and between", () => {
    expect(tokenizeValue("a ${trigger.x} b ${item.y}")).toEqual([
      { kind: "text", text: "a " },
      { kind: "token", token: "${trigger.x}" },
      { kind: "text", text: " b " },
      { kind: "token", token: "${item.y}" },
    ]);
  });

  it("preserves bracketed upstream paths inside a token", () => {
    expect(tokenizeValue("${upstream.http_1.rows[0].id}")).toEqual([
      { kind: "token", token: "${upstream.http_1.rows[0].id}" },
    ]);
  });
});

describe("serializeEditable", () => {
  it("reads plain text back", () => {
    const root = document.createElement("div");
    root.append(document.createTextNode("hello"));
    expect(serializeEditable(root)).toBe("hello");
  });

  it("reads a chip's data-token, not its label", () => {
    const root = document.createElement("div");
    root.append(chip("${secret.API_KEY}", "API_KEY"));
    expect(serializeEditable(root)).toBe("${secret.API_KEY}");
  });

  it("round-trips mixed text + chip", () => {
    const root = document.createElement("div");
    root.append(
      document.createTextNode("Bearer "),
      chip("${secret.API_KEY}", "API_KEY"),
    );
    expect(serializeEditable(root)).toBe("Bearer ${secret.API_KEY}");
  });

  it("ignores <br> and recurses into wrapper elements", () => {
    const root = document.createElement("div");
    root.append(document.createElement("br"));
    const wrap = document.createElement("div");
    wrap.append(
      document.createTextNode("x "),
      chip("${trigger.y}", "y"),
    );
    root.append(wrap);
    expect(serializeEditable(root)).toBe("x ${trigger.y}");
  });

  it("is the inverse of a renderable value", () => {
    // Mirror what renderInto produces from a tokenized value, then serialize.
    const value = "Authorization: Bearer ${secret.TOKEN} done";
    const root = document.createElement("div");
    for (const seg of tokenizeValue(value)) {
      if (seg.kind === "text") root.append(document.createTextNode(seg.text));
      else root.append(chip(seg.token, "label"));
    }
    expect(serializeEditable(root)).toBe(value);
  });
});
