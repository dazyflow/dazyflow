/**
 * sheets_append_row — official scripted connector (replaces the native Go drop).
 * Appends rows (from the 'rows' input) to a Google Sheet tab.
 */

const SHEETS_API_BASE = "https://sheets.googleapis.com/v4";

export default {
  manifest: {
    id: "sheets_append_row",
    version: "2.0.0",
    label: "Sheets append rows",
    summary: "Append rows to a Google Sheet tab, with the first row picking column order from headers.",
    description:
      "Append rows to a Google Sheet. Rows come in on the 'rows' input; column order is the optional 'headers' input or derived (sorted) from row keys.",
    integration: "Google Sheets",
    category: "network",
    icon: "file-output",
    brandLogo: "/brands/sheets.svg",
    color: "#0F9D58",
    tags: ["sheets", "google", "append", "log", "etl"],
    requiresConnections: [{ kind: "oauth", name: "google", note: "Google OAuth — sheets/drive scope." }],
    inputs: [
      { port: "rows", label: "Rows", required: true, mime: ["application/json"] },
      { port: "headers", label: "Headers (column order)", mime: ["application/json"] },
    ],
    outputs: [{ port: "meta", label: "Append metadata", mime: ["application/json"] }],
    idempotent: false,
    retryPolicy: "exponential_backoff",
    paramsSchema: {
      type: "object",
      properties: {
        base_url: { type: "string", description: "Override the API host (proxy / self-hosted / testing)." },
        account: { type: "string", default: "default" },
        token: { type: "string", description: "Raw access token; overrides 'account'." },
        spreadsheet_id: { type: "string", description: "Sheet ID or full URL." },
        range: { type: "string", default: "Sheet1", description: "Sheet name or range; appends after the last populated row." },
        value_input_option: { type: "string", enum: ["USER_ENTERED", "RAW"], default: "USER_ENTERED" },
        insert_data_option: { type: "string", enum: ["INSERT_ROWS", "OVERWRITE"], default: "INSERT_ROWS" },
        timeout_ms: { type: "integer", default: 15000, minimum: 1 },
      },
      required: ["spreadsheet_id"],
    },
    examples: [
      { title: "Append to the default sheet", params: { account: "default", spreadsheet_id: "1AbcDE...", range: "Sheet1" }, notes: "Rows come in on the 'rows' input port." },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const base = String(p.base_url || SHEETS_API_BASE).replace(/\/+$/, "");
    const id = sheetId(p.spreadsheet_id);
    if (!id) throw new DropError("bad_param", "'spreadsheet_id' is required");
    const token = await googleToken(ctx, p);

    if (!ctx.inputs.has("rows")) throw new DropError("missing_input", "input port 'rows' is required");
    const rowsVal = ctx.inputs.get("rows");
    if (!Array.isArray(rowsVal)) throw new DropError("bad_input", "'rows' must be an array of objects");
    const rows: Record<string, any>[] = rowsVal;

    let headers: string[];
    const headersVal = ctx.inputs.has("headers") ? ctx.inputs.get("headers") : undefined;
    if (Array.isArray(headersVal)) headers = headersVal.map(String);
    else headers = deriveHeaders(rows);

    const values = rows.map((row) => headers.map((h) => (h in (row || {}) ? row[h] : "")));
    const range = String(p.range || "Sheet1");

    if (values.length === 0) {
      return { meta: { appended_rows: 0 } };
    }

    const res = await ctx.fetch(`${base}/spreadsheets/${encodeURIComponent(id)}/values/${encodeURIComponent(range)}:append`, {
      method: "POST",
      query: {
        valueInputOption: String(p.value_input_option || "USER_ENTERED"),
        insertDataOption: String(p.insert_data_option || "INSERT_ROWS"),
      },
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json; charset=utf-8" },
      body: JSON.stringify({ range, majorDimension: "ROWS", values }),
      timeoutMs: Number(p.timeout_ms) || 15000,
    });
    if (!res.ok) throw new DropError("sheets_error", `Sheets returned ${res.status}: ${sheetsErr(await res.text())}`);
    const parsed: any = await res.json();
    const u = parsed.updates || {};
    return {
      meta: {
        appended_rows: values.length,
        spreadsheet_id: id,
        updated_range: u.updatedRange || "",
        updated_rows: u.updatedRows || 0,
        updated_columns: u.updatedColumns || 0,
        updated_cells: u.updatedCells || 0,
      },
    };
  },
};

// deriveHeaders is the union of row keys, sorted (matches the SQL drops' rule).
function deriveHeaders(rows: Record<string, any>[]): string[] {
  const seen: Record<string, boolean> = {};
  for (const r of rows) for (const k of Object.keys(r || {})) seen[k] = true;
  return Object.keys(seen).sort();
}

function sheetId(raw: any): string {
  const s = String(raw || "").trim();
  const m = s.match(/\/d\/([a-zA-Z0-9-_]+)/);
  return m ? m[1] : s;
}

function sheetsErr(text: string): string {
  try {
    const e = JSON.parse(text);
    if (e && e.error && e.error.message) return e.error.message;
  } catch (_e) {
    // not JSON
  }
  return text.slice(0, 512);
}

async function googleToken(ctx: any, p: any): Promise<string> {
  const t = String(p.token || "").trim();
  if (t) return t;
  if (!ctx.auth) throw new DropError("auth", "no token supplied and OAuth is not configured");
  return ctx.auth.token("google", p.account || "default");
}
