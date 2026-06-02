/**
 * gmail_send_email — the pilot port of a production connector to a scripted drop.
 *
 * Behaviour-for-behaviour with the native Go drop it replaces: builds an RFC822
 * message (header-injection-safe, RFC 2047 encoded-word subject so non-ASCII
 * like "Hej från Hazyflow!" doesn't mojibake, multipart/mixed with base64
 * attachments + RFC 2231 filenames), base64url-encodes it the way Gmail's API
 * requires, and POSTs to users/me/messages/send. The OAuth-connected account is
 * the implicit sender ("me"); Gmail's API doesn't allow spoofing the From.
 *
 * It reaches the network only through ctx.fetch (SSRF-guarded), the token only
 * through ctx.auth, attachments only through ctx.inputs + ctx.files — the same
 * capability surface that becomes the container broker API later. No native Go.
 */

const GMAIL_API_BASE = "https://gmail.googleapis.com/gmail/v1";

export default {
  manifest: {
    id: "gmail_send_email",
    version: "2.0.0",
    label: "Gmail send email",
    summary:
      "Send an email through the user's connected Gmail account, in plain text or HTML, with optional attachments and threading.",
    description:
      "Send an email through your connected Gmail account. The body comes from the 'body' input or the 'body' param; set format to html for rich content. Wire any number of file-producing nodes into the variadic 'attachments' input to attach files — each ref's MIME + filename ride through to the recipient. The 'from' address is fixed to your authorized Google account.",
    integration: "Gmail",
    category: "network",
    icon: "mail",
    brandLogo: "/brands/gmail.svg",
    color: "#D14836",
    tags: ["gmail", "email", "send", "google", "attachments"],
    requiresConnections: [
      { kind: "oauth", name: "google", note: "Google OAuth — gmail.send scope." },
    ],
    inputs: [
      { port: "body", label: "Email body (overrides params.body)" },
      { port: "attachments", label: "Files to attach (wire zero or more file-producing nodes)", variadic: true },
    ],
    outputs: [{ port: "meta", label: "Delivery metadata", mime: ["application/json"] }],
    idempotent: false,
    retryPolicy: "exponential_backoff",
    paramsSchema: {
      type: "object",
      properties: {
        base_url: { type: "string", description: "Override the API host (proxy / self-hosted / testing)." },
        account: { type: "string", default: "default" },
        token: { type: "string", description: "Raw access token; overrides 'account'." },
        to: { type: "string", description: "Recipient address (or comma-separated list)." },
        cc: { type: "string" },
        bcc: { type: "string" },
        subject: { type: "string" },
        body: { type: "string", description: "Default body when the input port isn't wired." },
        format: { type: "string", enum: ["text", "html"], default: "text" },
        reply_to: { type: "string", description: "Reply-To header." },
        thread_id: { type: "string", description: "Gmail thread ID to thread this reply into." },
        timeout_ms: { type: "integer", default: 15000, minimum: 1 },
      },
      required: ["to"],
    },
    examples: [
      {
        title: "Plain-text alert",
        params: { to: "oncall@example.com", subject: "Build failed", body: "main is red", token: "${secret:GMAIL_OAUTH}" },
      },
      {
        title: "HTML newsletter to a list",
        params: { to: "team@example.com", cc: "leads@example.com", subject: "Weekly digest", body: "<h1>Highlights</h1>", format: "html", token: "${secret:GMAIL_OAUTH}" },
      },
      {
        title: "Daily report with a PDF attachment",
        params: { to: "me@example.com", subject: "Yesterday's comments", body: "Comments digest attached.", token: "${secret:GMAIL_OAUTH}" },
        notes: "Wire a file-producing node (e.g. sheets_export_pdf) into the variadic 'attachments' input.",
      },
    ],
  },

  async run(ctx: any) {
    const p = ctx.params || {};
    const base = String(p.base_url || GMAIL_API_BASE).replace(/\/+$/, "");

    const to = String(p.to || "").trim();
    if (!to) throw new DropError("bad_param", "'to' is required");

    // Token: an explicit param wins (covers ${secret:…} templating); otherwise
    // the host-refreshed OAuth token for the gmail integration.
    let token = String(p.token || "").trim();
    if (!token) {
      if (!ctx.auth) throw new DropError("auth", "no token supplied and OAuth is not configured");
      token = await ctx.auth.token("google", p.account || "default");
    }
    if (!token) throw new DropError("auth", "empty access token");

    // Body: the input port overrides params.body. A wired ref is inline text or
    // a file-backed path we read through the sandbox.
    let body = String(p.body || "");
    if (ctx.inputs.has("body")) {
      const ref = ctx.inputs.ref("body");
      if (typeof ref.value === "string") body = ref.value;
      else if (ref.path) body = await ctx.files.readText(ref.path);
      else throw new DropError("bad_input", "body input must be text");
    }
    if (!body) throw new DropError("bad_input", "no body — set params.body or wire the 'body' input port");

    const bodyContentType =
      p.format === "html" ? 'text/html; charset="utf-8"' : 'text/plain; charset="utf-8"';

    const attachments = await loadAttachments(ctx);

    const raw = buildRFC822(
      ctx,
      {
        to,
        cc: String(p.cc || ""),
        bcc: String(p.bcc || ""),
        subject: String(p.subject || "(no subject)"),
        replyTo: String(p.reply_to || ""),
        bodyContentType,
      },
      body,
      attachments,
    );

    const payload: any = {
      // Gmail requires base64url WITHOUT padding for the raw message.
      raw: ctx.crypto.base64(ctx.crypto.utf8(raw), { url: true, pad: false }),
    };
    if (p.thread_id) payload.threadId = String(p.thread_id);

    const res = await ctx.fetch(`${base}/users/me/messages/send`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${token}`,
        "Content-Type": "application/json; charset=utf-8",
      },
      body: JSON.stringify(payload),
      timeoutMs: Number(p.timeout_ms) || 15000,
    });

    if (!res.ok) {
      const errText = await res.text();
      throw new DropError("gmail_error", `Gmail returned ${res.status}: ${extractGmailError(errText)}`);
    }

    const parsed: any = await res.json();
    return { meta: { id: parsed.id || "", threadId: parsed.threadId || "" } };
  },
};

// ───────────────────────────────────────────────────────────── attachments ──

interface Attachment {
  filename: string;
  mime: string;
  base64: string; // standard base64, already wrapped at 76 columns
}

async function loadAttachments(ctx: any): Promise<Attachment[]> {
  const refs = ctx.inputs.all("attachments");
  const out: Attachment[] = [];
  for (let i = 0; i < refs.length; i++) {
    const r = refs[i];
    const bytes =
      r.value !== undefined && r.value !== null ? r.value : await ctx.files.read(r.path);
    out.push({
      filename: attachmentFilename(r, i),
      mime: r.mime || "application/octet-stream",
      base64: wrap76(ctx.crypto.base64(bytes)), // std base64, padded
    });
  }
  return out;
}

function attachmentFilename(ref: any, idx: number): string {
  if (ref.path) {
    const base = ref.path.split("/").pop();
    if (base && base !== "." ) return base;
  }
  return `attachment-${idx + 1}${extForMIME(ref.mime || "")}`;
}

function extForMIME(mime: string): string {
  switch (mime.split(";")[0].trim().toLowerCase()) {
    case "application/pdf": return ".pdf";
    case "text/plain": return ".txt";
    case "text/csv": return ".csv";
    case "text/html": return ".html";
    case "application/json": return ".json";
    case "image/png": return ".png";
    case "image/jpeg": return ".jpg";
    case "application/zip": return ".zip";
  }
  return ".bin";
}

// ─────────────────────────────────────────────────────────── RFC822 builder ──

interface Headers {
  to: string;
  cc: string;
  bcc: string;
  subject: string;
  replyTo: string;
  bodyContentType: string;
}

function buildRFC822(ctx: any, h: Headers, body: string, attachments: Attachment[]): string {
  const lines: string[] = [];
  const header = (name: string, value: string) => {
    if (!value) return;
    // Header-injection defense: a value like "x@y\r\nBcc: leak@z" must not
    // smuggle extra headers.
    lines.push(`${name}: ${value.replace(/[\r\n]/g, "")}`);
  };

  header("To", h.to);
  header("Cc", h.cc);
  header("Bcc", h.bcc);
  header("Reply-To", h.replyTo);
  header("Subject", encodeWord(ctx, h.subject));
  header("MIME-Version", "1.0");

  if (attachments.length === 0) {
    header("Content-Type", h.bodyContentType);
    header("Content-Transfer-Encoding", "8bit");
    return lines.join("\r\n") + "\r\n\r\n" + body;
  }

  const boundary = "hazyflow-" + ctx.crypto.hex(ctx.crypto.randomBytes(16));
  header("Content-Type", `multipart/mixed; boundary="${boundary}"`);

  const parts: string[] = [lines.join("\r\n") + "\r\n"];

  // Body part.
  parts.push(
    `--${boundary}\r\n` +
      `Content-Type: ${h.bodyContentType}\r\n` +
      `Content-Transfer-Encoding: 8bit\r\n\r\n` +
      body +
      "\r\n",
  );

  // Attachment parts.
  for (const a of attachments) {
    parts.push(
      `--${boundary}\r\n` +
        `Content-Type: ${a.mime.replace(/[\r\n]/g, "")}\r\n` +
        `Content-Disposition: ${dispositionHeader(ctx, a.filename)}\r\n` +
        `Content-Transfer-Encoding: base64\r\n\r\n` +
        a.base64 +
        "\r\n",
    );
  }
  parts.push(`--${boundary}--\r\n`);
  return parts.join("");
}

// encodeWord applies RFC 2047 Q-encoding when a subject contains non-ASCII, and
// leaves pure-ASCII subjects untouched (mirrors Go's mime.QEncoding).
function encodeWord(ctx: any, s: string): string {
  if (isAscii(s)) return s;
  const bytes = utf8Bytes(s);
  let enc = "";
  for (const b of bytes) {
    if (b === 0x20) enc += "_";
    else if (b >= 0x21 && b <= 0x7e && b !== 0x3d && b !== 0x3f && b !== 0x5f)
      enc += String.fromCharCode(b);
    // RFC 2045 quoted-printable mandates uppercase hex digits (=C3=A5),
    // matching the native drop's mime.QEncoding output.
    else enc += "=" + hex2(b).toUpperCase();
  }
  return `=?utf-8?q?${enc}?=`;
}

// dispositionHeader emits a plain quoted filename for ASCII names and the RFC
// 2231 extended form (filename*=utf-8''…) for non-ASCII (mirrors Go's
// mime.FormatMediaType), so "årsrapport.pdf" survives to the recipient.
function dispositionHeader(_ctx: any, filename: string): string {
  if (isAscii(filename)) {
    return `attachment; filename="${filename.replace(/[\r\n"]/g, "")}"`;
  }
  let pct = "";
  for (const b of utf8Bytes(filename)) {
    if (isAttrChar(b)) pct += String.fromCharCode(b);
    else pct += "%" + hex2(b).toUpperCase();
  }
  return `attachment; filename*=utf-8''${pct}`;
}

// ───────────────────────────────────────────────────────────────── helpers ──

function isAscii(s: string): boolean {
  for (let i = 0; i < s.length; i++) if (s.charCodeAt(i) > 127) return false;
  return true;
}

// utf8Bytes encodes a JS string to UTF-8 as a plain number array — no runtime
// TextEncoder/Uint8Array iteration assumptions.
function utf8Bytes(s: string): number[] {
  const out: number[] = [];
  for (let i = 0; i < s.length; i++) {
    let c = s.charCodeAt(i);
    if (c < 0x80) out.push(c);
    else if (c < 0x800) out.push(0xc0 | (c >> 6), 0x80 | (c & 0x3f));
    else if (c >= 0xd800 && c <= 0xdbff) {
      const c2 = s.charCodeAt(++i);
      const cp = 0x10000 + ((c - 0xd800) << 10) + (c2 - 0xdc00);
      out.push(0xf0 | (cp >> 18), 0x80 | ((cp >> 12) & 0x3f), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
    } else out.push(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 0x3f), 0x80 | (c & 0x3f));
  }
  return out;
}

function hex2(b: number): string {
  return (b < 16 ? "0" : "") + b.toString(16);
}

// RFC 2231 attr-char set (the bytes that may appear unencoded in filename*).
function isAttrChar(b: number): boolean {
  if ((b >= 0x30 && b <= 0x39) || (b >= 0x41 && b <= 0x5a) || (b >= 0x61 && b <= 0x7a)) return true;
  return "!#$&+-.^_`|~".indexOf(String.fromCharCode(b)) >= 0;
}

function wrap76(b64: string): string {
  const rows: string[] = [];
  for (let i = 0; i < b64.length; i += 76) rows.push(b64.slice(i, i + 76));
  return rows.join("\r\n");
}

function extractGmailError(body: string): string {
  try {
    const j = JSON.parse(body);
    if (j && j.error && j.error.message) return String(j.error.message);
  } catch (_e) {
    // not JSON — fall through
  }
  return body.slice(0, 200);
}
