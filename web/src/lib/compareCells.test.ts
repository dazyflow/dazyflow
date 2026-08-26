// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Mirrors the sort_rows cases in drops/transform/sort_dedupe_test.go, because
// the two comparators answer the same question about the same data and a reader
// who sorts a collection in the app and then sorts it in a flow must not get
// two different answers.
import { describe, expect, it } from "vitest";
import { compareCells, sortRowsByColumn } from "./compareCells";

const order = (vals: unknown[]) => vals.slice().sort((a, b) => compareCells(a, b, "en"));

describe("compareCells", () => {
  it("orders string-encoded numbers numerically", () => {
    // The bug a plain string compare gives you, and the reason this exists: a
    // collection's store is all TEXT.
    expect(order(["10", "2", "1", "100"])).toEqual(["1", "2", "10", "100"]);
  });

  it("orders real numbers, and mixes them with numeric strings", () => {
    expect(order([10, "9", 2])).toEqual([2, "9", 10]);
  });

  it("treats a number with trailing text as text", () => {
    // "12 kr" is not 12: half-parsing it would call it equal to "12".
    expect(compareCells("12 kr", "12", "en")).not.toBe(0);
  });

  it("puts blanks first whichever way the column is sorted", () => {
    const rows = [{ v: "b" }, { v: "" }, { v: "a" }, { v: null }];
    expect(sortRowsByColumn(rows, "v", false, "en").map((r) => r.v)).toEqual([
      "",
      null,
      "a",
      "b",
    ]);
    expect(sortRowsByColumn(rows, "v", true, "en").map((r) => r.v)).toEqual([
      "",
      null,
      "b",
      "a",
    ]);
  });

  it("orders false before true", () => {
    expect(order([true, false])).toEqual([false, true]);
  });

  it("collates letters by the reader's own alphabet", () => {
    // The point of passing a locale, in the one comparison that shows it:
    // Swedish puts å/ä/ö at the END of the alphabet, after z, while English
    // treats Å as a decorated A. Byte order agrees with neither (it puts every
    // accented letter after Z, and after every lowercase letter too).
    const by = (locale: string) =>
      ["Zorn", "Åsa", "Anna"].slice().sort((a, b) => compareCells(a, b, locale));
    expect(by("sv")).toEqual(["Anna", "Zorn", "Åsa"]);
    expect(by("en")).toEqual(["Anna", "Åsa", "Zorn"]);
  });

  it("orders a number inside a string by value, not by digit", () => {
    expect(order(["item10", "item2"])).toEqual(["item2", "item10"]);
  });
});

describe("sortRowsByColumn", () => {
  const rows = [
    { name: "Carol", spend: "100" },
    { name: "Alice", spend: "20" },
    { name: "Bob", spend: "100" },
  ];

  it("does not mutate the array it is given", () => {
    const before = rows.map((r) => r.name);
    sortRowsByColumn(rows, "name", true, "en");
    expect(rows.map((r) => r.name)).toEqual(before);
  });

  it("is stable, so one sort survives the next", () => {
    // Sort by name, then by spend: rows sharing a spend keep their name order.
    const byName = sortRowsByColumn(rows, "name", false, "en");
    const bySpend = sortRowsByColumn(byName, "spend", false, "en");
    expect(bySpend.map((r) => r.name)).toEqual(["Alice", "Bob", "Carol"]);
  });

  it("orders a missing column as all-blank rather than throwing", () => {
    expect(sortRowsByColumn(rows, "nope", false, "en").map((r) => r.name)).toEqual([
      "Carol",
      "Alice",
      "Bob",
    ]);
  });
});
