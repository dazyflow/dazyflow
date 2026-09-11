// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from "vitest";
import { consoleLine, consoleLines } from "./consoleLog";

// The escapes are the point of the format — a reader finds the one error in
// two hundred lines by colour — so the tests read them literally rather than
// stripping them and asserting on the words that are left.
const DIM = "\x1b[2m";
const RESET = "\x1b[0m";
const RED = "\x1b[31m";

describe("consoleLine", () => {
  it("leaves a line with no level exactly as it arrived", () => {
    expect(consoleLine("\x1b[32mbuilt in 2s\x1b[0m")).toBe(
      "\x1b[32mbuilt in 2s\x1b[0m",
    );
  });

  it("puts the script line in the gutter and colours the level", () => {
    expect(consoleLine("boom", "error", 7)).toBe(
      `${DIM}  7 │${RESET} ${RED}error${RESET} boom`,
    );
  });

  // The common case pays nothing for the rare one: no tag, and no room held
  // open for the tag it does not print.
  it("puts console.log's message straight after the gutter", () => {
    expect(consoleLine("hello", "log", 1)).toBe(`${DIM}  1 │${RESET} hello`);
  });

  it("claims no line number when the sandbox itself speaks", () => {
    expect(consoleLine("… console output stopped after 200 lines", "warn", 0)).toContain(
      `${DIM}    │${RESET}`,
    );
  });
});

describe("consoleLines", () => {
  it("renders the rows a finished step recorded", () => {
    const rows = [
      { index: 0, line: 1, level: "log", message: "one" },
      { index: 1, line: 2, level: "error", message: "two" },
    ];
    expect(consoleLines(rows)).toEqual([
      consoleLine("one", "log", 1),
      consoleLine("two", "error", 2),
    ]);
  });

  // A port carries whatever a run put there, and this one is reached before
  // anything has validated it.
  it("skips what is not a log row, and gives nothing for what is not a list", () => {
    expect(consoleLines([null, 3, ["x"], { index: 0 }, { message: "kept" }])).toEqual([
      "kept",
    ]);
    expect(consoleLines(undefined)).toEqual([]);
    expect(consoleLines("logs")).toEqual([]);
  });
});
