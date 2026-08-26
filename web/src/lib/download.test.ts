// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// jsdom implements neither the object-URL methods nor Blob.text(), so the file
// contents are captured from the Blob constructor and the URL calls are
// recorded by hand.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { downloadJson, downloadText } from "./download";

type Recorded = { parts: string[]; type: string };

let written: Recorded[];
let created: number;
let revoked: string[];
let clicked: { href: string; download: string; inDocument: boolean }[];

const g = globalThis as unknown as Record<string, unknown>;
const urlObj = URL as unknown as Record<string, unknown>;
let origBlob: unknown;
let origCreate: unknown;
let origRevoke: unknown;
let origClick: unknown;

beforeEach(() => {
  written = [];
  created = 0;
  revoked = [];
  clicked = [];
  origBlob = g.Blob;
  origCreate = urlObj.createObjectURL;
  origRevoke = urlObj.revokeObjectURL;
  origClick = HTMLAnchorElement.prototype.click;

  g.Blob = class {
    constructor(parts: string[], opts?: { type?: string }) {
      written.push({ parts, type: opts?.type ?? "" });
    }
  };
  urlObj.createObjectURL = () => `blob:${++created}`;
  urlObj.revokeObjectURL = (u: string) => revoked.push(u);
  HTMLAnchorElement.prototype.click = function (this: HTMLAnchorElement) {
    clicked.push({
      href: this.getAttribute("href") ?? "",
      download: this.getAttribute("download") ?? "",
      // Firefox ignores a click on a detached anchor, so being in the document
      // at click time is part of the contract, not an implementation detail.
      inDocument: document.body.contains(this),
    });
  };
});

afterEach(() => {
  g.Blob = origBlob;
  urlObj.createObjectURL = origCreate;
  urlObj.revokeObjectURL = origRevoke;
  HTMLAnchorElement.prototype.click = origClick as typeof HTMLAnchorElement.prototype.click;
});

describe("downloadText", () => {
  it("saves the text under the given name and type", () => {
    downloadText("a,b\n1,2", "text/csv;charset=utf-8", "rows.csv");
    expect(written).toEqual([{ parts: ["a,b\n1,2"], type: "text/csv;charset=utf-8" }]);
    expect(clicked).toEqual([{ href: "blob:1", download: "rows.csv", inDocument: true }]);
  });

  it("releases the blob URL and leaves no anchor behind", () => {
    downloadText("x", "text/plain", "x.txt");
    expect(revoked).toEqual(["blob:1"]);
    expect(document.querySelectorAll("a")).toHaveLength(0);
  });

  it("releases the blob URL even when the click throws", () => {
    HTMLAnchorElement.prototype.click = function () {
      throw new Error("popup blocked");
    };
    expect(() => downloadText("x", "text/plain", "x.txt")).toThrow("popup blocked");
    // Without the finally, the blob would be pinned for the life of the page.
    expect(revoked).toEqual(["blob:1"]);
  });
});

describe("downloadJson", () => {
  it("writes indented JSON as application/json", () => {
    downloadJson({ a: 1, b: [2] }, "data.json");
    expect(written[0].type).toBe("application/json");
    // Indented on purpose: an export a person may open and read.
    expect(written[0].parts[0]).toBe('{\n  "a": 1,\n  "b": [\n    2\n  ]\n}');
    expect(clicked[0].download).toBe("data.json");
  });
});
