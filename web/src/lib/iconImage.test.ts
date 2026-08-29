// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { isImageIcon, fileToIconDataURL } from "./iconImage";

describe("isImageIcon", () => {
  // The branch decides HOW an icon renders: an image goes through <img>, a
  // logical name goes through iconFor. Getting it wrong either shows a broken
  // image or prints a data: URL as text.
  it("recognises image references", () => {
    for (const icon of [
      "data:image/svg+xml;base64,PHN2Zy8+",
      "data:image/png;base64,iVBORw0KGgo=",
      "https://cdn.example.com/logo.png",
      "http://example.com/logo.svg",
      "/assets/logo.svg", // absolute asset path
      "logo.svg",
      "logo.png",
      "logo.webp",
      "logo.jpg",
      "logo.jpeg",
      "LOGO.PNG", // extension match is case-insensitive
      "a/b/c/icon.SVG",
    ]) {
      expect(isImageIcon(icon), icon).toBe(true);
    }
  });

  it("treats a logical lucide name as not an image", () => {
    for (const icon of [
      "sparkles",
      "mail",
      "credit-card",
      "arrow-up-right",
      "svg", // the bare word is a name, not an extension
      "png",
      "my.icon",
    ]) {
      expect(isImageIcon(icon), icon).toBe(false);
    }
  });

  it("treats missing and empty as not an image", () => {
    expect(isImageIcon(undefined)).toBe(false);
    expect(isImageIcon("")).toBe(false);
  });

  // A non-image data: URL is not an image reference — only data:image/ is. This
  // matters because the value reaches an <img src>, so a data:text/html here
  // would be a value the branch should not opt into.
  it("only accepts data: URLs that declare an image type", () => {
    expect(isImageIcon("data:image/webp;base64,AA")).toBe(true);
    expect(isImageIcon("data:text/html,<script>alert(1)</script>")).toBe(false);
    expect(isImageIcon("data:application/json,{}")).toBe(false);
  });
});

describe("fileToIconDataURL", () => {
  function file(name: string, type: string, bytes: number): File {
    // A real Blob body so File.size is genuine rather than stubbed.
    return new File([new Uint8Array(bytes)], name, { type });
  }

  it("reads an SVG verbatim as a data: URL", async () => {
    const svg = new File(['<svg xmlns="http://www.w3.org/2000/svg"/>'], "logo.svg", {
      type: "image/svg+xml",
    });
    const got = await fileToIconDataURL(svg);
    expect(got.startsWith("data:")).toBe(true);
    expect(got).toContain("image/svg+xml");
  });

  // Type is taken from the MIME type OR the filename, because a file picked on
  // some platforms arrives with an empty type.
  it("accepts an SVG identified only by its extension", async () => {
    const svg = new File(["<svg/>"], "logo.svg", { type: "" });
    await expect(fileToIconDataURL(svg)).resolves.toContain("data:");
  });

  it("rejects an SVG over 64 KB", async () => {
    await expect(fileToIconDataURL(file("big.svg", "image/svg+xml", 64 * 1024 + 1)))
      .rejects.toThrow(/SVG is too large/i);
  });

  it("accepts an SVG exactly at the 64 KB cap", async () => {
    // The cap is exclusive (`> SVG_MAX_BYTES`), so the boundary value passes.
    await expect(fileToIconDataURL(file("edge.svg", "image/svg+xml", 64 * 1024)))
      .resolves.toContain("data:");
  });

  // The PNG cap is checked BEFORE any decoding, which is the point — an
  // oversized upload must not be handed to the decoder at all.
  it("rejects a PNG over 2 MB without decoding it", async () => {
    await expect(fileToIconDataURL(file("big.png", "image/png", 2 * 1024 * 1024 + 1)))
      .rejects.toThrow(/too large \(max 2 MB\)/i);
  });

  it("rejects anything that is not an SVG or PNG", async () => {
    for (const f of [
      file("photo.jpg", "image/jpeg", 10),
      file("photo.webp", "image/webp", 10),
      file("notes.txt", "text/plain", 10),
      file("payload.html", "text/html", 10),
      file("noext", "", 10),
    ]) {
      await expect(fileToIconDataURL(f), f.name).rejects.toThrow(/choose an SVG or PNG/i);
    }
  });

  // The message is shown to a person in the upload dialog, so it has to name
  // what to do rather than surface a type string.
  it("gives a user-facing message on the wrong type", async () => {
    await expect(fileToIconDataURL(file("a.gif", "image/gif", 10)))
      .rejects.toThrow("Please choose an SVG or PNG image.");
  });
});
