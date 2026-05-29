/**
 * sheets_export_pdf — official scripted connector (replaces the native Go drop).
 * Renders a Google Sheet to PDF (Drive files.export), writes it to the run's
 * sandbox, and returns a file ref a downstream node (e.g. gmail_send_email's
 * attachments port) can pick up.
 */

const DRIVE_API_BASE = "https://www.googleapis.com/drive/v3";

export default {
  manifest: {
    id: "sheets_export_pdf",
    version: "2.0.0",
    label: "Sheets export PDF",
    summary: "Export a Google Sheet to a PDF in the workspace sandbox, ready to attach to an email.",
    description:
      "Render a Google Sheet as a PDF and stash it in the run's sandbox so a downstream node (typically gmail_send_email's attachments port) can pick it up.",
    integration: "Google Sheets",
    category: "network",
    icon: "file-output",
    brandLogo: "/brands/sheets.svg",
    color: "#0F9D58",
    tags: ["sheets", "google", "pdf", "export", "drive"],
    requiresConnections: [{ kind: "oauth", name: "google", note: "Google OAuth — drive.readonly scope." }],
    outputs: [
      { port: "pdf", label: "PDF file ref", mime: ["application/pdf"] },
      { port: "meta", label: "Export metadata", mime: ["application/json"] },
    ],
    idempotent: true,
    paramsSchema: {
      type: "object",
      properties: {
        base_url: { type: "string", description: "Override the API host (proxy / self-hosted / testing)." },
        account: { type: "string", default: "default" },
        token: { type: "string", description: "Raw access token; overrides 'account'." },
        spreadsheet_id: { type: "string", description: "Sheet ID or full URL." },
        path: { type: "string", description: "Sandbox destination. Defaults to scratch://sheet-<id>.pdf." },
        timeout_ms: { type: "integer", default: 30000, minimum: 1 },
      },
      required: ["spreadsheet_id"],
    },
    examples: [
      { title: "Daily report into scratch for gmail_send_email", params: { account: "default", spreadsheet_id: "1AbcDE..." }, notes: "Wire the 'pdf' output into gmail_send_email's 'attachments' port." },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const base = String(p.base_url || DRIVE_API_BASE).replace(/\/+$/, "");
    const id = sheetId(p.spreadsheet_id);
    if (!id) throw new DropError("bad_param", "'spreadsheet_id' is required");
    const token = await googleToken(ctx, p);
    const dest = String(p.path || `scratch://sheet-${id}.pdf`);

    const res = await ctx.fetch(`${base}/files/${encodeURIComponent(id)}/export`, {
      query: { mimeType: "application/pdf" },
      headers: { Authorization: `Bearer ${token}` },
      timeoutMs: Number(p.timeout_ms) || 30000,
    });
    if (!res.ok) {
      let detail = await res.text();
      try {
        const e = JSON.parse(detail);
        if (e && e.error && e.error.message) detail = e.error.message;
      } catch (_e) {
        // not JSON
      }
      throw new DropError("sheets_error", `Drive export returned ${res.status}: ${detail}`);
    }
    const pdf = await res.bytes();
    await ctx.files.write(dest, pdf);
    return {
      pdf: { mime: "application/pdf", path: dest },
      meta: { spreadsheet_id: id, path: dest, bytes: pdf.length, mime: "application/pdf" },
    };
  },
};

function sheetId(raw: any): string {
  const s = String(raw || "").trim();
  const m = s.match(/\/d\/([a-zA-Z0-9-_]+)/);
  return m ? m[1] : s;
}

async function googleToken(ctx: any, p: any): Promise<string> {
  const t = String(p.token || "").trim();
  if (t) return t;
  if (!ctx.auth) throw new DropError("auth", "no token supplied and OAuth is not configured");
  return ctx.auth.token("google", p.account || "default");
}
