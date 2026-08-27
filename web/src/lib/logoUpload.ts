// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Turning a file an admin picked into the one thing the daemon stores: an
// inlined `data:` image, small.
//
// The size limit is not arbitrary and cannot be raised away. A catalog's mark
// lands on the manifest of EVERY operation it contributes, and the catalog
// response the editor loads carries each manifest in full — so the limit
// multiplies by up to sixty. engine/webapi/icon.go holds the same number.
//
// Rather than hand that budget to the admin as an error message, an oversized
// raster is redrawn here before it ever leaves the browser: a logo that was a
// 400 KiB screenshot becomes a couple of kilobytes, and the cap stops being
// something anyone has to think about. An image that already fits is passed
// through untouched, so a mark someone prepared properly keeps its own pixels.

// MAX_LOGO_BYTES mirrors maxLogoBytes in engine/webapi/icon.go. The daemon is
// the authority — this copy exists so the browser can shrink an image instead of
// round-tripping a rejection.
export const MAX_LOGO_BYTES = 16 * 1024;

// LOGO_BOX is what an oversized raster is redrawn to. The mark renders at about
// 32px, so 64 covers a 2x display with nothing to spare.
const LOGO_BOX = 64;

// LOGO_ACCEPT is the file picker's filter, and the same set the daemon inlines.
export const LOGO_ACCEPT =
  "image/png,image/jpeg,image/webp,image/gif,image/svg+xml,image/x-icon,image/vnd.microsoft.icon";

const LOGO_TYPES = new Set(LOGO_ACCEPT.split(","));

// dataURIBytes is the size of what a data: URI actually carries, which is what
// the cap is on — the base64 text is a third larger and comparing that would
// reject images that fit.
export function dataURIBytes(uri: string): number {
  const payload = uri.slice(uri.indexOf(",") + 1);
  const padding = payload.endsWith("==") ? 2 : payload.endsWith("=") ? 1 : 0;
  return Math.max(0, Math.floor((payload.length * 3) / 4) - padding);
}

// LogoError is the vocabulary fileToLogo rejects with. Codes rather than
// sentences: the page owns the wording, and every one of these needs a different
// suggestion attached to it.
export type LogoError = "unsupportedType" | "svgTooBig" | "tooBig" | "unreadable";

// fileToLogo reads a picked file and returns the data: URI to save, shrinking it
// if it does not fit. It throws an Error whose message is a LogoError code.
export async function fileToLogo(file: File): Promise<string> {
  if (!LOGO_TYPES.has(file.type)) {
    throw new Error("unsupportedType");
  }
  const original = await readDataURI(file);
  if (dataURIBytes(original) <= MAX_LOGO_BYTES) {
    return original;
  }
  if (file.type === "image/svg+xml") {
    // An SVG cannot be redrawn smaller — its size is its markup, and a 30 KiB
    // SVG is usually a traced illustration rather than a logo. Saying so beats
    // rasterising someone's scalable mark behind their back.
    throw new Error("svgTooBig");
  }
  const redrawn = await redraw(original);
  if (dataURIBytes(redrawn) > MAX_LOGO_BYTES) {
    throw new Error("tooBig");
  }
  return redrawn;
}

function readDataURI(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onerror = () => reject(new Error("unreadable"));
    reader.onload = () => {
      const out = reader.result;
      if (typeof out !== "string" || !out.startsWith("data:")) {
        reject(new Error("unreadable"));
        return;
      }
      resolve(out);
    };
    reader.readAsDataURL(file);
  });
}

// redraw fits the image into a transparent LOGO_BOX square and re-encodes it as
// PNG. Never upscales: a 24px favicon padded out to 64 would be four times the
// bytes for exactly the same picture.
async function redraw(dataURI: string): Promise<string> {
  const img = await loadImage(dataURI);
  const canvas = document.createElement("canvas");
  canvas.width = LOGO_BOX;
  canvas.height = LOGO_BOX;
  const ctx = canvas.getContext("2d");
  if (!ctx) {
    throw new Error("tooBig");
  }
  const scale = Math.min(LOGO_BOX / img.width, LOGO_BOX / img.height, 1);
  const w = Math.max(1, Math.round(img.width * scale));
  const h = Math.max(1, Math.round(img.height * scale));
  ctx.drawImage(img, (LOGO_BOX - w) / 2, (LOGO_BOX - h) / 2, w, h);
  return canvas.toDataURL("image/png");
}

function loadImage(src: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error("unreadable"));
    img.src = src;
  });
}
