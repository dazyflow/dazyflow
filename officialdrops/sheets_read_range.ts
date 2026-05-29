/**
 * sheets_read_range — official scripted connector (replaces the native Go drop).
 * Reads a tab/A1 range from a Google Sheet into rows + headers.
 */

const SHEETS_API_BASE = "https://sheets.googleapis.com/v4";

export default {
  manifest: {
    id: "sheets_read_range",
    version: "2.0.0",
    label: "Sheets read range",
    summary: "Read a tab or A1 range from a Google Sheet into rows + headers for downstream drops.",
    description:
      "Read a range from a Google Sheet into rows. The first row becomes the headers by default (override with headers=false for synthetic col_0/col_1 names).",
    integration: "Google Sheets",
    category: "network",
    icon: "file-input",
    brandLogo: "/brands/sheets.svg",
    color: "#0F9D58",
    tags: ["sheets", "google", "read", "etl"],
    requiresConnections: [{ kind: "oauth", name: "google", note: "Google OAuth — sheets/drive scope." }],
    outputs: [
      { port: "rows", label: "Rows", mime: ["application/json"] },
      { port: "headers", label: "Headers", mime: ["application/json"] },
    ],
    idempotent: true,
    paramsSchema: {
      type: "object",
      properties: {
        base_url: { type: "string", description: "Override the API host (proxy / self-hosted / testing)." },
        account: { type: "string", default: "default" },
        token: { type: "string", description: "Raw access token; overrides 'account'." },
        spreadsheet_id: { type: "string", description: "Sheet ID or full URL (the ID is extracted)." },
        range: { type: "string", default: "Sheet1", description: 'Sheet name ("Sheet1") or full range ("Sheet1!A1:D100").' },
        headers: { type: "boolean", default: true, description: "When true, the first row is treated as column headers." },
        value_render_option: { type: "string", enum: ["FORMATTED_VALUE", "UNFORMATTED_VALUE", "FORMULA"], default: "FORMATTED_VALUE" },
        timeout_ms: { type: "integer", default: 15000, minimum: 1 },
      },
      required: ["spreadsheet_id"],
    },
    examples: [
      { title: "Read the whole first sheet", params: { account: "default", spreadsheet_id: "1AbcDE...", range: "Sheet1", headers: true } },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const base = String(p.base_url || SHEETS_API_BASE).replace(/\/+$/, "");
    const id = sheetId(p.spreadsheet_id);
    if (!id) throw new DropError("bad_param", "'spreadsheet_id' is required");
    const token = await googleToken(ctx, p);
    const range = String(p.range || "Sheet1");

    const res = await ctx.fetch(`${base}/spreadsheets/${encodeURIComponent(id)}/values/${encodeURIComponent(range)}`, {
      query: { valueRenderOption: String(p.value_render_option || "FORMATTED_VALUE"), majorDimension: "ROWS" },
      headers: { Authorization: `Bearer ${token}` },
      timeoutMs: Number(p.timeout_ms) || 15000,
    });
    if (!res.ok) throw new DropError("sheets_error", `Sheets returned ${res.status}: ${sheetsErr(await res.text())}`);
    const parsed: any = await res.json();
    const values: any[][] = Array.isArray(parsed.values) ? parsed.values : [];
    const { headers, rows } = flatten(values, p.headers !== false);
    return { rows, headers };
  },
};

function flatten(raw: any[][], useHeaders: boolean): { headers: string[]; rows: any[] } {
  if (raw.length === 0) return { headers: [], rows: [] };
  let headers: string[];
  let data: any[][];
  if (useHeaders) {
    headers = raw[0].map((v) => (v === null || v === undefined ? "" : String(v)));
    data = raw.slice(1);
  } else {
    let maxCols = 0;
    for (const r of raw) if (r.length > maxCols) maxCols = r.length;
    headers = Array.from({ length: maxCols }, (_v, i) => `col_${i}`);
    data = raw;
  }
  const rows = data.map((r) => {
    const rec: Record<string, any> = {};
    headers.forEach((h, i) => {
      rec[h] = i < r.length ? r[i] : "";
    });
    return rec;
  });
  return { headers, rows };
}

// sheetId extracts the spreadsheet ID from a pasted URL, or returns the input.
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
