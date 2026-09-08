// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { glyphFor, languageOf } from "./langBadge";


describe("languageOf", () => {
  it("reads the language a node has chosen", () => {
    expect(languageOf({ language: "sql" }, "language", "plain")).toBe("sql");
  });

  it("says nothing when the node is on the param's own default", () => {
    // The two steps spell "no language chosen" differently — Text's default is
    // "plain", the runner step's is "default" — so the test is against the
    // SCHEMA's default rather than a list of no-op words this would have to be
    // updated for every time a step joined in.
    expect(languageOf({ language: "plain" }, "language", "plain")).toBe("");
    expect(languageOf({ shell: "default" }, "shell", "default")).toBe("");
    expect(languageOf({ shell: "python" }, "shell", "default")).toBe("python");
  });

  it("says nothing when there is nothing to read", () => {
    expect(languageOf(undefined, "language", "plain")).toBe("");
    expect(languageOf({}, "language", "plain")).toBe("");
    expect(languageOf({ language: "" }, "language", "plain")).toBe("");
    expect(languageOf({ language: 3 }, "language", "plain")).toBe("");
    expect(languageOf({ language: "sql" }, undefined, "plain")).toBe("");
  });
});

describe("glyphFor", () => {
  it("groups languages by what KIND of thing they are", () => {
    // The glyph groups and the label identifies — because only three of the
    // languages on offer have a mark anyone would recognise, and a row of three
    // real brand marks beside four invented ones reads as broken.
    expect(glyphFor("shell")).toBe("terminal");
    expect(glyphFor("bash")).toBe("terminal");
    expect(glyphFor("powershell")).toBe("terminal");
    expect(glyphFor("sql")).toBe("database");
    expect(glyphFor("json")).toBe("braces");
    expect(glyphFor("yaml")).toBe("text");
    expect(glyphFor("python")).toBe("code");
    expect(glyphFor("javascript")).toBe("code");
  });

  it("gives an unknown language the generic code glyph, not nothing", () => {
    expect(glyphFor("klingon")).toBe("code");
    expect(glyphFor("")).toBe("code");
  });
});
