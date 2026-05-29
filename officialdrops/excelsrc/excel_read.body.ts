/**
 * excel_read — read rows from an .xlsx workbook in the workspace.
 *
 * This is the BODY half of the drop; the generator (excelsrc/gen.go) prepends
 * the SheetJS bundle as `const XLSX = …` ahead of this file to produce the
 * embedded officialdrops/excel_read.ts. Don't edit the generated file — edit
 * this body (or the vendored xlsx.full.min.js) and re-run `go generate`.
 *
 * Pure-JS: replaces the former native excelize drop. Same id, ports, and
 * params, so existing graphs keep resolving. SheetJS's community build has no
 * cell styling, but reading is faithful.
 */

// Strip the legacy "workspace://" scheme the native drop used; ctx.files takes a
// bare workspace-relative path (or "scratch://…").
function wsPath(p: any): string {
  const s = String(p || "").trim();
  return s.startsWith("workspace://") ? s.slice("workspace://".length) : s;
}

// ctx.files.read returns the file bytes; normalize to a Uint8Array for SheetJS
// regardless of how the host surfaces them (Buffer / typed array / ArrayBuffer).
function toU8(x: any): Uint8Array {
  if (x instanceof Uint8Array) return x;
  if (x instanceof ArrayBuffer) return new Uint8Array(x);
  return Uint8Array.from(x as any);
}

export default {
  manifest: {
    id: "excel_read",
    version: "2.0.0",
    label: "Excel: read",
    summary:
      "Read rows from an .xlsx workbook in the workspace; the first row is the headers by default.",
    description:
      "Read an .xlsx workbook from the workspace into a row stream. The first row becomes the object keys (headers) unless headers:false. Restrict to a cell range (e.g. \"A1:D100\") or skip leading rows; flip on typed mode for native numbers/dates/booleans instead of strings.",
    integration: "Excel",
    category: "io",
    icon: "file-spreadsheet",
    brandLogo: "/brands/excel.svg",
    tags: ["excel", "xlsx", "spreadsheet", "read"],
    inputs: [
      { port: "path", label: "Workspace path (overrides params.path when wired)", mime: ["text/plain"] },
    ],
    outputs: [
      { port: "rows", label: "Rows", mime: ["application/json"] },
      { port: "headers", label: "Headers", mime: ["application/json"] },
    ],
    idempotent: true,
    paramsSchema: {
      type: "object",
      properties: {
        path: { type: "string", description: "Workspace-relative path to the .xlsx. Ignored if a 'path' input is wired." },
        sheet: { type: "string", description: "Sheet name; defaults to the first sheet." },
        headers: { type: "boolean", description: "Treat the first row as headers (default true). False → rows are arrays." },
        skip: { type: "integer", description: "Skip this many leading rows before reading." },
        range: { type: "string", description: "Cell range, e.g. \"A1:D100\"." },
        typed: { type: "boolean", description: "Return native numbers/dates/booleans instead of strings." },
      },
    },
    examples: [
      { title: "Read a sheet with headers", params: { path: "workspace://reports/sales.xlsx", sheet: "Sheet1", headers: true } },
      { title: "Typed, skipping a banner", params: { path: "workspace://exports/q3.xlsx", range: "A3:F500", typed: true } },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const wired = ctx.inputs.has("path") ? ctx.inputs.get("path") : null;
    const path = wsPath(wired || p.path);
    if (!path) throw new DropError("bad_param", "'path' is required");

    const wb = XLSX.read(toU8(await ctx.files.read(path)), {
      type: "array",
      cellDates: !!p.typed,
    });
    const sheetName = p.sheet || wb.SheetNames[0];
    const ws = wb.Sheets[sheetName];
    if (!ws) {
      throw new DropError("no_sheet", `sheet ${JSON.stringify(sheetName)} not found; workbook has: ${JSON.stringify(wb.SheetNames)}`);
    }

    const opts: any = { defval: null, raw: !!p.typed };
    if (p.range) opts.range = p.range;
    else if (typeof p.skip === "number" && p.skip > 0) opts.range = p.skip;

    if (p.headers === false) {
      return { rows: XLSX.utils.sheet_to_json(ws, { ...opts, header: 1 }), headers: [] };
    }
    const rows = XLSX.utils.sheet_to_json(ws, opts);
    let headers = rows.length ? Object.keys(rows[0]) : [];
    if (!headers.length) {
      const first = XLSX.utils.sheet_to_json(ws, { ...opts, header: 1 })[0];
      headers = Array.isArray(first) ? first.map(String) : [];
    }
    return { rows, headers };
  },
};
