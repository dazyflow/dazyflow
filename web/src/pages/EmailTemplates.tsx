// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Plus, Send, Trash2 } from "lucide-react";
import { api, APIError } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { useAuth } from "../auth";
import { Button } from "../components/Button";
import { ConfirmModal } from "../components/ConfirmModal";
import type { EmailTemplateSummary } from "../types";
import { ErrorNotice } from "../components/ErrorNotice";

// EmailTemplates is the "Email templates" tab of Admin → Secrets: the org's
// library of reusable HTML layout shells the email drops wrap a body in.
// Built-in templates are global and read-only (view + preview only); org
// templates are editable. The list panel sits left, an editor + live preview
// on the right. Mirrors the Secrets tab's structure (list + add/delete, gated
// on secret:write) and uses a server-rendered preview so {{.Body}}/{{.Logo}}
// render faithfully.

// A raw NUL byte used to live in this literal, which made git treat the whole
// file as BINARY: its diffs were invisible without --text and any concurrent
// edit became an unresolvable binary conflict. The escape reads identically to
// the compiler and keeps the file text.
const NEW_DRAFT = "\u0000new"; // sentinel selection id for an unsaved new template

export function EmailTemplates() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  // Managing the library is admin-only (org-wide branding, live reference);
  // non-admins can still view and pick templates when building flows.
  const canWrite = hasPerm("organization:admin");

  const [templates, setTemplates] = useState<EmailTemplateSummary[] | null>(null);
  const [off, setOff] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [pendingDelete, setPendingDelete] = useState<string | null>(null);

  const refresh = () => {
    if (!token) return;
    setTemplates(null);
    api
      .listEmailTemplates(token)
      .then((r) => {
        setTemplates(r.templates);
        setOff(false);
      })
      .catch((e) => {
        if (e instanceof APIError && (e.status === 501 || e.status === 403 || e.status === 401))
          setOff(true);
        else setError(explainApiError(e, t));
      });
  };

  useEffect(refresh, [token]);

  const current = templates?.find((tpl) => tpl.id === selected) ?? null;
  const creating = selected === NEW_DRAFT;

  return (
    <div className="page email-templates">
      <h1>{t("emailTemplates.title", "Email templates")}</h1>
      <p className="page-sub">
        {t(
          "emailTemplates.intro",
          "Reusable HTML layouts the email steps wrap a message body in — your branding, header and footer in one place. Built-in templates are read-only; edits to your own apply to every flow that uses them.",
        )}
      </p>
      {off ? (
        <div className="card">{t("emailTemplates.off", "Email templates are unavailable on this install.")}</div>
      ) : (
        <>
          {error && <ErrorNotice>{error}</ErrorNotice>}
          <div className="email-templates-layout">
        <div className="email-templates-list">
          {canWrite && (
            <Button variant="primary" onClick={() => setSelected(NEW_DRAFT)}>
              <Plus size={14} /> {t("emailTemplates.new", "New template")}
            </Button>
          )}
          {templates === null ? (
            <p className="muted">{t("common.loading", "Loading…")}</p>
          ) : (
            <ul>
              {templates.map((tpl) => (
                <li key={tpl.id}>
                  <button
                    type="button"
                    className={tpl.id === selected ? "active" : ""}
                    onClick={() => setSelected(tpl.id)}
                  >
                    <span>{tpl.name}</span>
                    {tpl.builtin && <span className="badge">{t("emailTemplates.builtin", "Built-in")}</span>}
                  </button>
                  {canWrite && !tpl.readOnly && (
                    <Button
                      variant="ghost"
                      size="icon"
                      className="danger"
                      aria-label={t("common.delete", "Delete")}
                      onClick={() => setPendingDelete(tpl.id)}
                    >
                      <Trash2 size={14} />
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="email-templates-editor">
          {creating ? (
            <TemplateEditor
              key={NEW_DRAFT}
              token={token}
              canWrite={canWrite}
              onSaved={(id) => {
                refresh();
                setSelected(id);
              }}
            />
          ) : current ? (
            <TemplateEditor
              key={current.id}
              token={token}
              canWrite={canWrite && !current.readOnly}
              existing={current}
              onSaved={refresh}
            />
          ) : (
            <p className="muted">{t("emailTemplates.pick", "Select a template, or create a new one.")}</p>
          )}
        </div>
      </div>

      {pendingDelete && (
        <ConfirmModal
          title={t("emailTemplates.deleteTitle", "Delete template?")}
          message={t("emailTemplates.deleteBody", "Flows referencing “{{name}}” will fail until pointed at another template.", {
            name: pendingDelete,
          })}
          confirmLabel={t("common.delete", "Delete")}
          danger
          onConfirm={() => {
            const name = pendingDelete;
            setPendingDelete(null);
            if (!token || !name) return;
            api
              .deleteEmailTemplate(token, name)
              .then(() => {
                if (selected === name) setSelected(null);
                refresh();
              })
              .catch((e) => setError(explainApiError(e, t)));
          }}
          onCancel={() => setPendingDelete(null)}
        />
      )}
        </>
      )}
    </div>
  );
}

// TemplateEditor edits one template (or a new draft). Built-ins arrive with
// canWrite=false: the HTML and preview show, but name/HTML are read-only and
// Save is hidden. The preview is server-rendered (debounced) so it matches a
// real send including {{if .Logo}} blocks.
function TemplateEditor({
  token,
  canWrite,
  existing,
  onSaved,
}: {
  token: string | null;
  canWrite: boolean;
  existing?: EmailTemplateSummary;
  onSaved: (id: string) => void;
}) {
  const { t } = useTranslation();
  const { me, hasPerm } = useAuth();
  const [name, setName] = useState(existing?.id ?? "");
  const [displayName, setDisplayName] = useState(existing?.name ?? "");
  const [html, setHtml] = useState(existing?.html ?? DEFAULT_TEMPLATE_HTML);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [preview, setPreview] = useState<string>("");

  // "Send test" delivers this shell to one address through the org's Email
  // connection. Pre-fill the recipient with the caller's own email (the
  // common case is "send it to me"); gated on secret:write since it reads the
  // stored SMTP credentials and actually sends.
  const canSendTest = hasPerm("secret:write");
  const [testTo, setTestTo] = useState(me?.subject ?? "");
  const [testBusy, setTestBusy] = useState(false);
  const [testResult, setTestResult] = useState<string | null>(null);
  const [testError, setTestError] = useState<string | null>(null);

  // Debounced server-side preview of the current HTML.
  useEffect(() => {
    if (!token) return;
    const handle = window.setTimeout(() => {
      api
        // Sample subject so a {{.Subject}} slot renders something in the editor
        // preview (body falls back to sample content server-side).
        .previewEmailTemplate(token, { html, subject: "Sample subject" })
        .then((r) => setPreview(r.html))
        .catch(() => setPreview("")); // a parse error just blanks the preview
    }, 300);
    return () => window.clearTimeout(handle);
  }, [token, html]);

  const save = () => {
    if (!token) return;
    setErr(null);
    setBusy(true);
    api
      .putEmailTemplate(token, name.trim(), html, displayName.trim() || undefined)
      .then(() => onSaved(name.trim()))
      .catch((e) => setErr(explainApiError(e, t)))
      .finally(() => setBusy(false));
  };

  const sendTest = () => {
    if (!token) return;
    setTestError(null);
    setTestResult(null);
    setTestBusy(true);
    api
      // Send the current editor HTML so unsaved edits are tested as-is, with a
      // subject naming the template for context in the inbox.
      .sendTestEmail(token, {
        to: testTo.trim() || undefined,
        html,
        subject: t("emailTemplates.testSubject", "Test: {{name}}", {
          name: displayName.trim() || name.trim() || "email template",
        }),
      })
      .then((r) => setTestResult(t("emailTemplates.testSent", "Sent to {{to}}.", { to: r.to })))
      .catch((e) => setTestError(explainApiError(e, t)))
      .finally(() => setTestBusy(false));
  };

  const isNew = !existing;

  return (
    <div className="template-editor">
      <label>
        {t("emailTemplates.name", "Name")}
        <input
          type="text"
          value={isNew ? name : existing.id}
          disabled={!isNew || !canWrite}
          placeholder="welcome"
          onChange={(e) => {
            setName(e.target.value);
            if (!displayName) setDisplayName(e.target.value);
          }}
        />
      </label>
      <label>
        {t("emailTemplates.displayName", "Display name")}
        <input
          type="text"
          value={displayName}
          disabled={!canWrite}
          placeholder={t("emailTemplates.displayNamePlaceholder", "Welcome email")}
          onChange={(e) => setDisplayName(e.target.value)}
        />
      </label>
      <label>
        {t("emailTemplates.html", "Template HTML")}
        <textarea
          className="template-html-input"
          rows={16}
          value={html}
          disabled={!canWrite}
          spellCheck={false}
          onChange={(e) => setHtml(e.target.value)}
        />
      </label>
      <p className="muted">
        {t("emailTemplates.bodyHint", "Use {{.Body}} where the email body goes. {{.Subject}} and {{.Logo}} are also available.")}
      </p>

      {err && <p className="field-error">{err}</p>}
      {canWrite && (
        <Button variant="primary" onClick={save} disabled={busy || name.trim() === ""}>
          {t("common.save", "Save")}
        </Button>
      )}

      {canSendTest && (
        <div className="template-test-send">
          <label>
            {t("emailTemplates.testTo", "Send a test to")}
            <div className="template-test-send-row">
              <input
                type="email"
                value={testTo}
                placeholder={t("emailTemplates.testToPlaceholder", "you@example.com")}
                onChange={(e) => {
                  setTestTo(e.target.value);
                  setTestResult(null);
                  setTestError(null);
                }}
              />
              <Button
                variant="secondary"
                onClick={sendTest}
                disabled={testBusy || testTo.trim() === ""}
              >
                <Send size={14} /> {t("emailTemplates.sendTest", "Send test")}
              </Button>
            </div>
          </label>
          <p className="muted">
            {t(
              "emailTemplates.testHint",
              "Sends sample content wrapped in this template through your Email connection, so you can see how it lands in a real inbox.",
            )}
          </p>
          {testResult && <p className="field-ok">{testResult}</p>}
          {testError && <p className="field-error">{testError}</p>}
        </div>
      )}

      <div className="template-preview">
        <h3>{t("emailTemplates.preview", "Preview")}</h3>
        <iframe title={t("emailTemplates.preview", "Preview")} srcDoc={preview} sandbox="" />
      </div>
    </div>
  );
}

// DEFAULT_TEMPLATE_HTML seeds a new template with a minimal valid shell that
// already has the required {{.Body}} placeholder.
const DEFAULT_TEMPLATE_HTML = `<!DOCTYPE html>
<html>
  <body style="margin:0;padding:24px;background:#f4f5fa;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#1b2233;">
    <div style="max-width:600px;margin:0 auto;background:#fff;border:1px solid #e9ebf3;border-radius:12px;padding:32px;">
      {{.Body}}
    </div>
  </body>
</html>`;
