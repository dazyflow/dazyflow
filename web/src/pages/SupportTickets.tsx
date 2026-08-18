// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { AlertCircle, LifeBuoy, Send, ArrowLeft, Check, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api, APIError } from "../api";
import { Button } from "../components/Button";
import { BundleView } from "../components/BundleView";
import { explainApiError } from "../lib/explainApiError";
import { formatDateTime } from "../lib/datetime";
import type { SupportBundle, Ticket, TicketMessage, TicketStatus, TicketView } from "../types";

// THREAD_POLL_MS re-fetches an open ticket so a reply from the other party shows
// up without a manual reload. 8s is responsive enough for a chat without
// hammering the API; the nav badge (AppShell) polls more slowly for the count.
const THREAD_POLL_MS = 8000;

// SupportTickets.tsx is the native ticket + chat surface (Phase 2 of the Support
// feature). Three views share one file because they share most of the chat UI:
//   - SupportTickets: an org member's own list of tickets + "New ticket".
//   - SupportQueue:   the cross-tenant support-agent queue.
//   - TicketThread:   one ticket's conversation, reused by user + agent via the
//                     `mode` prop (which decides the API calls + affordances).
// Everything a user types is secret-scrubbed server-side before it is stored.

const NOTICE_STYLE = { color: "var(--muted)" } as const;

// statusLabel resolves a ticket status to friendly copy (support.status.*).
function useStatusLabel() {
  const { t } = useTranslation();
  return (s: TicketStatus) => t(`support.status.${s}`);
}

// ---- End-user ticket list --------------------------------------------------

export function SupportTickets() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const statusLabel = useStatusLabel();
  const navigate = useNavigate();
  const [tickets, setTickets] = useState<Ticket[]>([]);
  const [loading, setLoading] = useState(true);
  const [disabled, setDisabled] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.listMyTickets(token);
      setTickets(r.tickets ?? []);
      setDisabled(false);
      setError(null);
    } catch (e) {
      if (e instanceof APIError && e.status === 501) setDisabled(true);
      else setError(explainApiError(e, t));
    } finally {
      setLoading(false);
    }
  }, [token, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <LifeBuoy size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("support.title")}
          </h1>
          <div className="sub">
            {t("support.subtitle")}
          </div>
        </div>
        {!disabled && (
          <Button variant="primary" onClick={() => setCreating(true)}>
            {t("support.new")}
          </Button>
        )}
      </div>

      {error && (
        <div className="card" style={{ color: "var(--danger)", marginBottom: "var(--space-4)" }}>
          <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
          {error}
        </div>
      )}

      {disabled ? (
        <div className="card" style={NOTICE_STYLE}>
          {t("support.notEnabled")}
        </div>
      ) : loading && tickets.length === 0 ? (
        <div className="card" style={NOTICE_STYLE}>{t("common.loading")}</div>
      ) : tickets.length === 0 ? (
        <div className="card" style={NOTICE_STYLE}>
          {t("support.empty")}
        </div>
      ) : (
        <div className="user-list">
          {tickets.map((tk) => (
            <Link key={tk.id} to={`/support/${encodeURIComponent(tk.id)}`} className="user-card" style={{ textDecoration: "none" }}>
              <div style={{ minWidth: 0 }}>
                <div className="subject">{tk.subject}</div>
                <div className="meta">
                  <span className="count-pill" style={{ marginRight: 8 }}>{statusLabel(tk.status)}</span>
                  {t("support.updated", { date: formatDateTime(tk.updated_at) })}
                </div>
              </div>
            </Link>
          ))}
        </div>
      )}

      {creating && (
        <NewTicketModal
          onClose={() => setCreating(false)}
          onCreated={(tk) => {
            setCreating(false);
            navigate(`/support/${encodeURIComponent(tk.id)}`);
          }}
        />
      )}
    </div>
  );
}

// NewTicketModal files a fresh ticket (no flow context — the RunDetail path
// supplies that separately via ReportProblemModal).
function NewTicketModal({ onClose, onCreated }: { onClose: () => void; onCreated: (t: Ticket) => void }) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [subject, setSubject] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const submit = async () => {
    if (!token || !subject.trim()) return;
    setBusy(true);
    setErr(null);
    try {
      const tk = await api.createTicket(token, { subject: subject.trim(), message: message.trim() || undefined });
      onCreated(tk);
    } catch (e) {
      setErr(explainApiError(e, t));
      setBusy(false);
    }
  };

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-label={t("support.new")}
        style={{ maxWidth: 520 }}
      >
        <div className="modal-head">
          <strong>{t("support.new")}</strong>
          <Button size="icon" onClick={onClose} aria-label={t("common.close")}>
            <X size={16} />
          </Button>
        </div>
        <div className="modal-body">
          <label className="field-label">{t("support.subjectLabel")}</label>
          <input
            className="input"
            value={subject}
            onChange={(e) => setSubject(e.target.value)}
            placeholder={t("support.subjectPlaceholder")}
            autoFocus
          />
          <label className="field-label" style={{ marginTop: "var(--space-3)" }}>
            {t("support.messageLabel")}
          </label>
          <textarea
            className="input"
            rows={5}
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            placeholder={t("support.messagePlaceholder")}
          />
          {err && <div className="card error" style={{ marginTop: "var(--space-3)" }}>{err}</div>}
        </div>
        <div className="modal-foot">
          <Button variant="ghost" onClick={onClose}>{t("common.cancel")}</Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy || !subject.trim()}>
            {t("support.send")}
          </Button>
        </div>
      </div>
    </div>
  );
}

// ---- Support-agent queue ---------------------------------------------------

export function SupportQueue() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();
  const statusLabel = useStatusLabel();
  const [tickets, setTickets] = useState<Ticket[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      const r = await api.listTicketQueue(token);
      setTickets(r.tickets ?? []);
      setError(null);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setLoading(false);
    }
  }, [token, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  if (!hasPerm("support:agent")) {
    return <div className="card" style={{ color: "var(--danger)" }}>{t("support.agentOnly")}</div>;
  }

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <LifeBuoy size={20} style={{ marginRight: 8, verticalAlign: -3 }} />
            {t("support.queueTitle")}
          </h1>
          <div className="sub">{t("support.queueSub")}</div>
        </div>
      </div>
      {error && (
        <div className="card" style={{ color: "var(--danger)", marginBottom: "var(--space-4)" }}>{error}</div>
      )}
      {loading && tickets.length === 0 ? (
        <div className="card" style={NOTICE_STYLE}>{t("common.loading")}</div>
      ) : tickets.length === 0 ? (
        <div className="card" style={NOTICE_STYLE}>{t("support.queueEmpty")}</div>
      ) : (
        <div className="user-list">
          {tickets.map((tk) => (
            <Link key={tk.id} to={`/support/queue/${encodeURIComponent(tk.id)}`} className="user-card" style={{ textDecoration: "none" }}>
              <div style={{ minWidth: 0 }}>
                <div className="subject">{tk.subject}</div>
                <div className="meta">
                  <span className="count-pill" style={{ marginRight: 8 }}>{statusLabel(tk.status)}</span>
                  {tk.tenant}
                  {" · "}
                  {formatDateTime(tk.updated_at)}
                </div>
              </div>
            </Link>
          ))}
        </div>
      )}
    </div>
  );
}

// ---- One ticket's thread (shared by user + agent) --------------------------

export function TicketThread({ mode }: { mode: "user" | "agent" }) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const statusLabel = useStatusLabel();
  const { id = "" } = useParams();
  const [view, setView] = useState<TicketView | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [showBundle, setShowBundle] = useState(false);
  const endRef = useRef<HTMLDivElement>(null);

  const load = useCallback(async () => {
    if (!token) return;
    try {
      const v = mode === "agent" ? await api.getSupportTicket(token, id) : await api.getMyTicket(token, id);
      setView(v);
      setError(null);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setLoading(false);
    }
  }, [token, id, mode, t]);

  useEffect(() => {
    void load();
  }, [load]);

  // Poll while the thread is open so a reply from the other party appears
  // without a manual reload. Silent (no spinner) — load() only sets error on a
  // hard failure. Draft state is separate, so a poll never clobbers what the
  // user is typing.
  useEffect(() => {
    const iv = window.setInterval(() => void load(), THREAD_POLL_MS);
    return () => window.clearInterval(iv);
  }, [load]);

  useEffect(() => {
    endRef.current?.scrollIntoView({ block: "end" });
  }, [view?.messages.length]);

  const send = async () => {
    if (!token || !draft.trim()) return;
    setBusy(true);
    try {
      const v =
        mode === "agent"
          ? await api.postSupportTicketMessage(token, id, draft.trim())
          : await api.postMyTicketMessage(token, id, draft.trim());
      setView(v);
      setDraft("");
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  };

  const setStatus = async (status: TicketStatus) => {
    if (!token) return;
    setBusy(true);
    try {
      setView(await api.setSupportTicketStatus(token, id, status));
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  };

  const backTo = mode === "agent" ? "/support/queue" : "/support";

  if (loading) return <div className="card" style={NOTICE_STYLE}>{t("common.loading")}</div>;
  if (error && !view) {
    return (
      <div>
        <Link to={backTo} className="back-link"><ArrowLeft size={14} /> {t("common.back")}</Link>
        <div className="card" style={{ color: "var(--danger)", marginTop: "var(--space-3)" }}>{error}</div>
      </div>
    );
  }
  if (!view) return null;
  const tk = view.ticket;
  const closed = tk.status === "resolved" || tk.status === "closed";

  return (
    <div style={{ maxWidth: 760 }}>
      <Link to={backTo} className="back-link"><ArrowLeft size={14} /> {t("common.back")}</Link>
      <div className="page-title" style={{ marginTop: "var(--space-2)" }}>
        <div>
          <h1 style={{ fontSize: "var(--text-xl)" }}>{tk.subject}</h1>
          <div className="sub">
            <span className="count-pill" style={{ marginRight: 8 }}>{statusLabel(tk.status)}</span>
            {tk.flow_id && (
              <Link to={`/flows/${encodeURIComponent(tk.flow_id)}`}>{t("support.viewFlow")}</Link>
            )}
            {tk.bundle_id && (
              <>
                {" · "}
                <button type="button" className="linklike" onClick={() => setShowBundle(true)}>
                  {t("support.viewBundle")}
                </button>
              </>
            )}
          </div>
        </div>
        {mode === "agent" && !closed && (
          <Button onClick={() => void setStatus("resolved")} disabled={busy}>
            <Check size={12} style={{ marginRight: 4 }} />
            {t("support.resolve")}
          </Button>
        )}
      </div>

      <div className="ticket-thread">
        {view.messages.map((m) => (
          <ChatBubble key={m.id} m={m} mode={mode} />
        ))}
        <div ref={endRef} />
      </div>

      {error && <div className="card error" style={{ marginTop: "var(--space-2)" }}>{error}</div>}

      <div className="ticket-composer">
        <textarea
          className="input"
          rows={2}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) void send();
          }}
          placeholder={
            closed
              ? t("support.reopenPlaceholder")
              : t("support.replyPlaceholder")
          }
        />
        <Button variant="primary" onClick={() => void send()} disabled={busy || !draft.trim()}>
          <Send size={14} style={{ marginRight: 4 }} />
          {t("support.send")}
        </Button>
      </div>

      {showBundle && (
        <BundleModal ticketId={id} mode={mode} onClose={() => setShowBundle(false)} />
      )}
    </div>
  );
}

// BundleModal fetches + renders the redacted diagnostic bundle attached to a
// ticket. Lazy: it only calls the API when opened.
function BundleModal({ ticketId, mode, onClose }: { ticketId: string; mode: "user" | "agent"; onClose: () => void }) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [bundle, setBundle] = useState<SupportBundle | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!token) return;
    (mode === "agent" ? api.getSupportTicketBundle(token, ticketId) : api.getMyTicketBundle(token, ticketId))
      .then(setBundle)
      .catch((e) => setError(explainApiError(e, t)));
  }, [token, ticketId, mode, t]);

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-label={t("bundle.title")}
        style={{ maxWidth: 680 }}
      >
        <div className="modal-head">
          <strong>{t("bundle.title")}</strong>
          <Button size="icon" onClick={onClose} aria-label={t("common.close")}>
            <X size={16} />
          </Button>
        </div>
        <div className="modal-body">
          {error && <div className="card error">{error}</div>}
          {!bundle && !error && <div style={NOTICE_STYLE}>{t("common.loading")}</div>}
          {bundle && <BundleView bundle={bundle} />}
        </div>
        <div className="modal-foot">
          <Button variant="ghost" onClick={onClose}>{t("common.close")}</Button>
        </div>
      </div>
    </div>
  );
}

// ChatBubble renders one message. "mine" = the reader's own side (right); the
// other party and system notes render on the left / centered.
function ChatBubble({ m, mode }: { m: TicketMessage; mode: "user" | "agent" }) {
  const { t } = useTranslation();
  if (m.author_kind === "system") {
    return (
      <div className="ticket-system-note">{m.body}</div>
    );
  }
  const mine = mode === "agent" ? m.author_kind === "support" : m.author_kind === "user";
  const who =
    m.author_kind === "support"
      ? t("support.fromSupport")
      : m.author || t("support.fromYou");
  return (
    <div className={"ticket-bubble" + (mine ? " mine" : "")}>
      <div className="ticket-bubble-meta">{who} · {formatDateTime(m.created_at)}</div>
      <div className="ticket-bubble-body">{m.body}</div>
    </div>
  );
}
