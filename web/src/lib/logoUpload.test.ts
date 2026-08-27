// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { dataURIBytes, fileToLogo, MAX_LOGO_BYTES } from "./logoUpload";

// A real 1x1 PNG, so the type check has something honest to pass.
const PNG = new Uint8Array([
  0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49,
  0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06,
  0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44,
  0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01, 0x0d,
  0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42,
  0x60, 0x82,
]);

const file = (bytes: BlobPart, type: string, name = "logo") =>
  new File([bytes], name, { type });

describe("dataURIBytes", () => {
  // The cap is on the IMAGE, not on the base64 text that carries it — which is
  // a third larger, so comparing that would reject images that fit.
  it("measures the payload, not the encoded string", () => {
    expect(dataURIBytes("data:image/png;base64,AAAA")).toBe(3);
    expect(dataURIBytes("data:image/png;base64,AAA=")).toBe(2);
    expect(dataURIBytes("data:image/png;base64,AA==")).toBe(1);
    expect(dataURIBytes("data:image/png;base64,")).toBe(0);
  });
});

describe("fileToLogo", () => {
  it("passes through an image that already fits", async () => {
    const uri = await fileToLogo(file(PNG, "image/png"));
    expect(uri.startsWith("data:image/png;base64,")).toBe(true);
    expect(dataURIBytes(uri)).toBeLessThanOrEqual(MAX_LOGO_BYTES);
  });

  it("accepts an SVG, which is the mark that scales", async () => {
    const svg = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"/>';
    const uri = await fileToLogo(file(svg, "image/svg+xml", "logo.svg"));
    expect(uri.startsWith("data:image/svg+xml")).toBe(true);
  });

  it("refuses a file that is not an image we can show", async () => {
    await expect(fileToLogo(file("%PDF-1.4", "application/pdf"))).rejects.toThrow(
      "unsupportedType",
    );
  });

  // An SVG's size is its markup, so there is nothing to shrink — and
  // rasterising someone's scalable mark behind their back would be the wrong
  // favour.
  it("refuses an oversized SVG rather than rasterising it", async () => {
    const bloated = `<svg xmlns="http://www.w3.org/2000/svg">${"<path d='M0 0'/>".repeat(
      3000,
    )}</svg>`;
    expect(bloated.length).toBeGreaterThan(MAX_LOGO_BYTES);
    await expect(
      fileToLogo(file(bloated, "image/svg+xml", "logo.svg")),
    ).rejects.toThrow("svgTooBig");
  });

  // The redraw path for an oversized raster is deliberately not tested here:
  // jsdom neither decodes an <img> nor implements a 2d canvas context, so what
  // it would exercise is the absence of a browser rather than the shrink.
});
