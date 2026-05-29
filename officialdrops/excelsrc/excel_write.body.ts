/**
 * excel_write — serialize a row stream into an .xlsx workbook in the workspace.
 *
 * BODY half — the generator prepends the SheetJS bundle as `const XLSX = …`.
 * Replaces the former native excelize drop (same id/ports/params). Note: the
 * SheetJS community build does no cell styling, so the native-only `autosize`
 * and `freezeRow` params are accepted but no-ops here.
 */

function wsPath(p: any): string {
  const s = String(p || "").trim();
  return s.startsWith("workspace://") ? s.slice("workspace://".length) : s;
}

function toU8(x: any): Uint8Array {
  if (x instanceof Uint8Array) return x;
  if (x instanceof ArrayBuffer) return new Uint8Array(x);
  return Uint8Array.from(x as any);
}

const XLSX_MIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet";

export default {
  manifest: {
    id: "excel_write",
    version: "2.0.0",
    label: "Excel: write",
    summary: "Serialize a row stream into an .xlsx workbook in the workspace, optionally appending to an existing sheet.",
    description:
      "Write a row stream (array of objects) to an .xlsx workbook in the workspace. Wire a 'headers' input to fix column order; otherwise columns are derived from the first row. With append:true, rows are added to an existing sheet of the same name. (autosize/freezeRow are accepted for compatibility but not applied — SheetJS has no styling.)",
    integration: "Excel",
    category: "io",
    icon: "file-spreadsheet",
    brandLogo: "/brands/excel.svg",
    tags: ["excel", "xlsx", "spreadsheet", "write"],
    inputs: [
      { port: "rows", label: "Rows", required: true, mime: ["application/json"] },
      { port: "headers", label: "Headers", required: false, mime: ["application/json"] },
    ],
    outputs: [{ port: "out", label: "Written path", mime: [XLSX_MIME] }],
    idempotent: false,
    paramsSchema: {
      type: "object",
      properties: {
        path: { type: "string", description: "Workspace-relative destination .xlsx." },
        sheet: { type: "string", description: "Sheet name (default \"Sheet1\")." },
        append: { type: "boolean", description: "Append rows to an existing sheet of the same name instead of overwriting." },
        autosize: { type: "boolean", description: "Accepted for compatibility; not applied (no styling)." },
        freezeRow: { type: "integer", description: "Accepted for compatibility; not applied (no styling)." },
      },
      required: ["path"],
    },
    examples: [
      { title: "Write a report", params: { path: "workspace://reports/sales-2026.xlsx", sheet: "Sales" } },
      { title: "Append to a log", params: { path: "workspace://logs/audit.xlsx", sheet: "Events", append: true } },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const path = wsPath(p.path);
    if (!path) throw new DropError("bad_param", "'path' is required");

    if (!ctx.inputs.has("rows")) throw new DropError("missing_input", "input port 'rows' is required");
    const rows = ctx.inputs.get("rows");
    if (!Array.isArray(rows)) throw new DropError("bad_input", "'rows' must be a JSON array of objects");
    const headersVal = ctx.inputs.has("headers") ? ctx.inputs.get("headers") : undefined;
    const headers = Array.isArray(headersVal) ? headersVal.map(String) : undefined;
    const sheet = String(p.sheet || "Sheet1");

    const sheetOpts = headers ? { header: headers } : undefined;

    let wb: any;
    if (p.append && (await ctx.files.exists(path))) {
      wb = XLSX.read(toU8(await ctx.files.read(path)), { type: "array" });
      const existing = wb.Sheets[sheet];
      if (existing) {
        XLSX.utils.sheet_add_json(existing, rows, { skipHeader: true, origin: -1, ...(headers ? { header: headers } : {}) });
      } else {
        XLSX.utils.book_append_sheet(wb, XLSX.utils.json_to_sheet(rows, sheetOpts), sheet);
      }
    } else {
      wb = XLSX.utils.book_new();
      XLSX.utils.book_append_sheet(wb, XLSX.utils.json_to_sheet(rows, sheetOpts), sheet);
    }

    // SheetJS type:"array" returns an ArrayBuffer; the host's files.write accepts
    // a Uint8Array directly.
    await ctx.files.write(path, toU8(XLSX.write(wb, { type: "array", bookType: "xlsx" })));
    return { out: { mime: XLSX_MIME, path } };
  },
};
