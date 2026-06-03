// Shared support for "real" icons (uploaded SVG / PNG) on flows and
// orgs. Both store the image inline as a data: URL in their existing
// string field (Graph.Icon / OrgProfile.icon) — no separate asset
// store. PNGs are downscaled client-side so the stored blob stays small;
// SVGs are tiny already. Images are always rendered via <img> (never
// inlined as markup), so an uploaded SVG can't execute scripts.

// Max accepted source file sizes. SVGs are kept small as-is; PNGs are
// downscaled before storage, but we still cap the source to avoid
// decoding a huge upload.
const SVG_MAX_BYTES = 64 * 1024;
const PNG_MAX_BYTES = 2 * 1024 * 1024;
// Longest edge (px) a PNG is downscaled to — icons render at ~16–40px,
// so 128 keeps them crisp on hi-dpi while bounding the data-URL size.
const PNG_MAX_EDGE = 128;

// isImageIcon reports whether an icon value is an image reference (a
// data: URL, an http(s) URL, an absolute asset path, or a filename with
// an image extension) rather than a logical lucide-icon name like
// "sparkles". Callers render images via <img> and names via iconFor.
export function isImageIcon(icon?: string): boolean {
  if (!icon) return false;
  return (
    /^(data:image\/|https?:\/\/|\/)/.test(icon) ||
    /\.(svg|png|webp|jpe?g)$/i.test(icon)
  );
}

// fileToIconDataURL validates an uploaded SVG/PNG and returns a data:
// URL suitable for storing in an icon field. SVGs are read verbatim
// (after a size check); PNGs are downscaled to PNG_MAX_EDGE first.
// Throws an Error with a user-facing message on the wrong type / too
// large.
export async function fileToIconDataURL(file: File): Promise<string> {
  const name = file.name.toLowerCase();
  const isSvg = file.type === "image/svg+xml" || name.endsWith(".svg");
  const isPng = file.type === "image/png" || name.endsWith(".png");

  if (isSvg) {
    if (file.size > SVG_MAX_BYTES) {
      throw new Error("That SVG is too large (max 64 KB).");
    }
    return readAsDataURL(file);
  }
  if (isPng) {
    if (file.size > PNG_MAX_BYTES) {
      throw new Error("That image is too large (max 2 MB).");
    }
    return downscaleToDataURL(file, PNG_MAX_EDGE);
  }
  throw new Error("Please choose an SVG or PNG image.");
}

function readAsDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(new Error("Couldn't read the file."));
    reader.readAsDataURL(file);
  });
}

function loadImage(src: string): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = new Image();
    img.onload = () => resolve(img);
    img.onerror = () => reject(new Error("Couldn't decode the image."));
    img.src = src;
  });
}

async function downscaleToDataURL(file: File, maxEdge: number): Promise<string> {
  const url = URL.createObjectURL(file);
  try {
    const img = await loadImage(url);
    const scale = Math.min(1, maxEdge / Math.max(img.width, img.height || 1));
    const w = Math.max(1, Math.round(img.width * scale));
    const h = Math.max(1, Math.round(img.height * scale));
    const canvas = document.createElement("canvas");
    canvas.width = w;
    canvas.height = h;
    const ctx = canvas.getContext("2d");
    if (!ctx) throw new Error("Your browser can't process images here.");
    ctx.drawImage(img, 0, 0, w, h);
    return canvas.toDataURL("image/png");
  } finally {
    URL.revokeObjectURL(url);
  }
}
