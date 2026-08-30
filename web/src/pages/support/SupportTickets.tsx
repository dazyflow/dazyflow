// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Check,
  Inbox,
  LifeBuoy,
  MessageSquare,
  Search,
  Send,
  UserCheck,
  UserMinus,
  X,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../../auth";
import { api, APIError } from "../../api";
import { BackLink } from "../../components/ui/BackLink";
import { Button } from "../../components/ui/Button";
import { BundleView } from "../../components/BundleView";
import { explainApiError } from "../../lib/explainApiError";
import { formatDateTime } from "../../lib/datetime";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import type {
  FlowSummary,
  SupportBundle,
  Ticket,
  TicketMessage,
  TicketQueueFilter,
  TicketQueueSummaryResponse,
  TicketStatus,
  TicketView,
} from "../../types";
import { ICON } from "../../icons";
import { POLL } from "../../lib/timing";
import { useEscapeToClose } from "../../components/ui/useEscapeToClose";
import { Loading } from "../../components/ui/Loading";
import { Notice } from "../../components/ui/Notice";
import { ConfirmModal } from "../../components/ui/ConfirmModal";

// An open ticket re-fetches itself so a reply from the other party shows up
// without a manual reload. It polls on the `watched` tier (see lib/timing) —
// responsive enough for a chat without hammering the API. The nav badge in
// AppShell counts the same tickets on the slower `background` tier, since that
// one only drives a number.

// SupportTickets.tsx is the native ticket + chat surface (Phase 2 of the Support
// feature). Three views share one file because they share most of the chat UI:
//   - SupportTickets: an org member's own list of tickets + "New ticket".
//   - SupportQueue:   the support agent's dashboard over the cross-org queue —
//                     stat tiles that double as filters, plus per-row claim.
//   - TicketThread:   one ticket's conversation, reused by user + agent via the
//                     `mode` prop (which decides the API calls + affordances).
// Everything a user types is secret-scrubbed server-side before it is stored, and
// the user-facing responses omit which support agent owns or answered a ticket.


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
            <LifeBuoy size={ICON.xl} />
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

      {error && <ErrorNotice style={{ marginBottom: "var(--space-4)" }}>{error}</ErrorNotice>}

      {disabled ? (
        <Notice>
          {t("support.notEnabled")}
        </Notice>
      ) : loading && tickets.length === 0 ? (
        <Loading />
      ) : tickets.length === 0 ? (
        <Notice>
          {t("support.empty")}
        </Notice>
      ) : (
        <div className="user-list">
          {tickets.map((tk) => (
            <Link key={tk.id} to={`/support/${encodeURIComponent(tk.id)}`} className="user-card" style={{ textDecoration: "none" }}>
              <div style={{ minWidth: 0 }}>
                <div className="subject">{tk.subject}</div>
                <div className="meta">
                  <span className="count-pill" style={{ marginRight: "var(--space-2)" }}>{statusLabel(tk.status)}</span>
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

// NewTicketModal files a fresh ticket. Picking a flow is optional but strongly
// worth it: the server then auto-attaches a redacted diagnostic bundle, which is
// what lets support diagnose WITHOUT asking for a live read-only grant. The
// RunDetail failure banner reaches the same endpoint via ReportProblemModal,
// where the flow + run are already known; here the user picks from their flows.
//
// We also look up that flow's most recent FAILED run and attach it, because a
// bundle with a run snapshot carries the per-step outcome and error code — the
// difference between "here's my flow" and "here's my flow and how it broke".
// Best effort: no failed run (or a lookup error) just means a structure-only
// bundle, never a blocked filing.
function NewTicketModal({ onClose, onCreated }: { onClose: () => void; onCreated: (t: Ticket) => void }) {
  const { t } = useTranslation();
  const { token, activeTenant, activeWorkspace } = useAuth();
  const [subject, setSubject] = useState("");
  const [message, setMessage] = useState("");
  const [flowId, setFlowId] = useState("");
  const [flows, setFlows] = useState<FlowSummary[]>([]);
  const [runId, setRunId] = useState("");
  const [runAt, setRunAt] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // The user's flows populate the picker. A failure here is silent: the picker
  // just stays hidden and the ticket is filed without flow context.
  useEffect(() => {
    if (!token || !activeWorkspace) return;
    let cancelled = false;
    api
      .listGraphs(token, activeTenant, activeWorkspace)
      .then((r) => {
        if (!cancelled) setFlows(r.graphs ?? []);
      })
      .catch(() => {
        if (!cancelled) setFlows([]);
      });
    return () => {
      cancelled = true;
    };
  }, [token, activeTenant, activeWorkspace]);

  // Look up the newest failed run for the chosen flow so the bundle can carry
  // the run outcome. Cleared when the user picks "no specific flow".
  useEffect(() => {
    setRunId("");
    setRunAt("");
    if (!token || !flowId || !activeWorkspace) return;
    let cancelled = false;
    api
      .listRuns(token, activeTenant, activeWorkspace, flowId, { status: "failed", limit: 1 })
      .then((r) => {
        const run = (r.runs ?? [])[0];
        if (cancelled || !run) return;
        setRunId(run.id);
        setRunAt(run.finished_at || run.enqueued_at || "");
      })
      .catch(() => {
        /* no run context — structure-only bundle */
      });
    return () => {
      cancelled = true;
    };
  }, [token, flowId, activeTenant, activeWorkspace]);

  const submit = async () => {
    if (!token || !subject.trim()) return;
    setBusy(true);
    setErr(null);
    try {
      const tk = await api.createTicket(token, {
        subject: subject.trim(),
        message: message.trim() || undefined,
        flow_id: flowId || undefined,
        run_id: runId || undefined,
      });
      onCreated(tk);
    } catch (e) {
      setErr(explainApiError(e, t));
      setBusy(false);
    }
  };


  useEscapeToClose(onClose);
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal"
        onClick={(e) => e.stopPropagation()}
        aria-modal="true"
        role="dialog"
        aria-label={t("support.new")}
        style={{ maxWidth: 520 }}
      >
        <div className="modal-head">
          <strong>{t("support.new")}</strong>
          <Button size="icon" onClick={onClose} aria-label={t("common.close")}>
            <X size={ICON.md} />
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
          {flows.length > 0 && (
            <>
              <label className="field-label" htmlFor="ticket-flow" style={{ marginTop: "var(--space-3)" }}>
                {t("support.flowLabel")}
              </label>
              <select
                id="ticket-flow"
                className="input"
                value={flowId}
                onChange={(e) => setFlowId(e.target.value)}
              >
                <option value="">{t("support.flowNone")}</option>
                {flows.map((f) => (
                  <option key={f.id} value={f.id}>
                    {f.name || f.id}
                  </option>
                ))}
              </select>
              {/* Say plainly what leaves the org the moment a flow is picked.
                  The premise of the product is "your stuff is secret", so the
                  attachment is never silent. */}
              <div className="sub" style={{ marginTop: "var(--space-2)" }}>
                {flowId ? t("support.flowAttachNote") : t("support.flowNoneNote")}
                {flowId && runId && (
                  <>
                    {" "}
                    {t("support.flowRunNote", {
                      date: runAt ? formatDateTime(runAt) : "",
                    })}
                  </>
                )}
              </div>
            </>
          )}
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
          {err && <ErrorNotice style={{ marginTop: "var(--space-3)" }}>{err}</ErrorNotice>}
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

// ---- Support-agent dashboard (the cross-org queue) -------------------------

// QueueView is one saved view of the queue. Each dashboard tile IS a view, so a
// tile both reports a number and is the way to reach the tickets behind it —
// clicking "3 unassigned" shows those three rather than leaving the agent to
// reconstruct the filter by hand.
type QueueView = { own: TicketQueueFilter; status: TicketStatus | "all" };

const VIEW_ALL: QueueView = { own: "all", status: "all" };
const VIEW_UNASSIGNED: QueueView = { own: "unassigned", status: "all" };
const VIEW_MINE: QueueView = { own: "mine", status: "all" };
const VIEW_WAITING: QueueView = { own: "all", status: "awaiting_support" };

function sameView(a: QueueView, b: QueueView): boolean {
  return a.own === b.own && a.status === b.status;
}

export function SupportQueue() {
  const { t } = useTranslation();
  const { token, me, hasPerm } = useAuth();
  const statusLabel = useStatusLabel();
  const [tickets, setTickets] = useState<Ticket[]>([]);
  const [summary, setSummary] = useState<TicketQueueSummaryResponse | null>(null);
  const [view, setView] = useState<QueueView>(VIEW_ALL);
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [claiming, setClaiming] = useState<string | null>(null);

  const refresh = useCallback(async () => {
    if (!token) return;
    setLoading(true);
    try {
      // The tiles are counted server-side across the WHOLE queue, so they stay
      // truthful no matter how the list below is filtered or how long it is.
      const [queue, counts] = await Promise.all([
        api.listTicketQueue(token, {
          status: view.status === "all" ? undefined : view.status,
          assignee: view.own === "mine" ? "me" : undefined,
          unassigned: view.own === "unassigned",
        }),
        api.ticketQueueSummary(token),
      ]);
      setTickets(queue.tickets ?? []);
      setSummary(counts);
      setError(null);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setLoading(false);
    }
  }, [token, t, view]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  // Claim from the list: triage without opening every ticket first.
  const claim = async (id: string) => {
    if (!token) return;
    setClaiming(id);
    try {
      await api.assignSupportTicket(token, id, "me");
      await refresh();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setClaiming(null);
    }
  };

  // Free text is matched client-side over the loaded page: the queue API filters
  // on status and ownership, not text, and the page is bounded server-side.
  const q = query.trim().toLowerCase();
  const visible = q
    ? tickets.filter((tk) =>
        [tk.subject, tk.tenant, tk.flow_id ?? "", tk.assigned_to ?? ""].some((field) =>
          field.toLowerCase().includes(q),
        ),
      )
    : tickets;

  if (!hasPerm("support:agent")) {
    return <ErrorNotice>{t("support.agentOnly")}</ErrorNotice>;
  }

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <LifeBuoy size={ICON.xl} />
            {t("support.queueTitle")}
          </h1>
          <div className="sub">{t("support.queueSub")}</div>
        </div>
        {/* The agent's other half of the feature: the flows an org has already
            consented to let them open read-only. */}
        <Link to="/support" className="dash-panel-link">{t("support.yourAccess")}</Link>
      </div>

      <div className="dash-stats">
        <QueueTile
          icon={<Inbox size={ICON.lg} />}
          label={t("support.stats.unassigned")}
          value={summary?.summary.unassigned}
          tone={summary && summary.summary.unassigned > 0 ? "warn" : "good"}
          active={sameView(view, VIEW_UNASSIGNED)}
          onSelect={() => setView(VIEW_UNASSIGNED)}
        />
        <QueueTile
          icon={<UserCheck size={ICON.lg} />}
          label={t("support.stats.mine")}
          value={summary?.mine}
          active={sameView(view, VIEW_MINE)}
          onSelect={() => setView(VIEW_MINE)}
        />
        <QueueTile
          icon={<MessageSquare size={ICON.lg} />}
          label={t("support.stats.waiting")}
          value={summary?.summary.by_status?.awaiting_support}
          active={sameView(view, VIEW_WAITING)}
          onSelect={() => setView(VIEW_WAITING)}
        />
        <QueueTile
          icon={<LifeBuoy size={ICON.lg} />}
          label={t("support.stats.open")}
          value={summary?.summary.open}
          sub={summary ? t("support.stats.ofTotal", { count: summary.summary.total }) : undefined}
          active={sameView(view, VIEW_ALL)}
          onSelect={() => setView(VIEW_ALL)}
        />
      </div>

      <div className="flow-toolbar">
        <div className="flow-search">
          <Search size={ICON.sm} aria-hidden />
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("support.searchPlaceholder")}
            aria-label={t("support.searchPlaceholder")}
          />
        </div>
        <label className="flow-filter">
          <span>{t("support.filterOwner")}</span>
          <select
            value={view.own}
            onChange={(e) => setView({ ...view, own: e.target.value as TicketQueueFilter })}
          >
            <option value="all">{t("support.ownerAll")}</option>
            <option value="unassigned">{t("support.ownerUnassigned")}</option>
            <option value="mine">{t("support.ownerMine")}</option>
          </select>
        </label>
        <label className="flow-filter">
          <span>{t("common.status")}</span>
          <select
            value={view.status}
            onChange={(e) =>
              setView({ ...view, status: e.target.value as TicketStatus | "all" })
            }
          >
            <option value="all">{t("support.statusAny")}</option>
            <option value="awaiting_support">{t("support.status.awaiting_support")}</option>
            <option value="awaiting_user">{t("support.status.awaiting_user")}</option>
            <option value="open">{t("support.status.open")}</option>
            <option value="resolved">{t("support.status.resolved")}</option>
            <option value="closed">{t("support.status.closed")}</option>
          </select>
        </label>
      </div>

      {error && (
        <ErrorNotice style={{ marginBottom: "var(--space-4)" }}>{error}</ErrorNotice>
      )}
      {loading && tickets.length === 0 ? (
        <Loading />
      ) : visible.length === 0 ? (
        <Notice>
          {/* "The queue is empty" is only true of the unfiltered view — a narrowed
              one that comes back empty means this view, not the whole queue. */}
          {tickets.length === 0
            ? sameView(view, VIEW_ALL)
              ? t("support.queueEmpty")
              : t("support.noneInView")
            : t("support.noMatches")}
        </Notice>
      ) : (
        <div className="user-list">
          {visible.map((tk) => (
            <QueueRow
              key={tk.id}
              ticket={tk}
              mySubject={me?.subject ?? ""}
              statusLabel={statusLabel}
              claiming={claiming === tk.id}
              onClaim={() => void claim(tk.id)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

// QueueTile is a dashboard stat that doubles as the filter for what it counts.
// A button rather than a Link: it changes the view in place instead of navigating.
function QueueTile({
  icon,
  label,
  value,
  sub,
  tone = "neutral",
  active,
  onSelect,
}: {
  icon: React.ReactNode;
  label: string;
  value: number | undefined;
  sub?: string;
  tone?: "neutral" | "good" | "warn" | "bad";
  active: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      className={
        "card dash-stat dash-stat-" + tone + " support-stat" + (active ? " is-active" : "")
      }
      aria-pressed={active}
      onClick={onSelect}
    >
      <span className="dash-stat-icon">{icon}</span>
      {/* An em dash while the counts load, so the tile never flashes a wrong 0. */}
      <span className="dash-stat-value">{value === undefined ? "—" : String(value)}</span>
      <span className="dash-stat-label">{label}</span>
      {sub && <span className="dash-stat-sub">{sub}</span>}
    </button>
  );
}

// QueueRow is one ticket in the queue: who filed it, who owns it, and — when
// nobody does yet — a one-click claim.
function QueueRow({
  ticket,
  mySubject,
  statusLabel,
  claiming,
  onClaim,
}: {
  ticket: Ticket;
  mySubject: string;
  statusLabel: (s: TicketStatus) => string;
  claiming: boolean;
  onClaim: () => void;
}) {
  const { t } = useTranslation();
  const owner = ticket.assigned_to ?? "";
  const mine = owner !== "" && owner === mySubject;
  return (
    <div className="user-card">
      <div style={{ minWidth: 0 }}>
        <div className="subject">
          <Link to={`/support/queue/${encodeURIComponent(ticket.id)}`} style={{ textDecoration: "none" }}>
            {ticket.subject}
          </Link>
        </div>
        <div className="meta">
          <span className="count-pill" style={{ marginRight: "var(--space-2)" }}>{statusLabel(ticket.status)}</span>
          {ticket.tenant}
          {" · "}
          {owner === ""
            ? t("support.unassigned")
            : mine
              ? t("support.assignedToYou")
              : t("support.assignedTo", { agent: owner })}
          {" · "}
          {formatDateTime(ticket.updated_at)}
        </div>
      </div>
      <div className="user-card-actions">
        {owner === "" && (
          <Button onClick={onClaim} disabled={claiming}>
            <UserCheck size={ICON.xs} />
            {t("support.claim")}
          </Button>
        )}
      </div>
    </div>
  );
}

// ---- One ticket's thread (shared by user + agent) --------------------------

export function TicketThread({ mode }: { mode: "user" | "agent" }) {
  const { t } = useTranslation();
  const { token, me } = useAuth();
  const statusLabel = useStatusLabel();
  const { id = "" } = useParams();
  const [view, setView] = useState<TicketView | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [showBundle, setShowBundle] = useState(false);
  // Closing is the requester saying "stop working on this", and it lands on
  // support's side as well as their own — near enough to irreversible in the
  // moment (it is undone by replying, not by an Undo) that a misclick on a
  // button sitting between Release and the composer should not do it.
  const [confirmClose, setConfirmClose] = useState(false);
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

  // Tell the server this thread was opened, so the reminder sweep can tell
  // "hasn't answered" from "hasn't even looked" — the difference between a
  // useful nudge and mail to someone who is already up to date.
  //
  // Once per mount, deliberately, and NOT on every poll: the read time only
  // has to be later than the newest message for the sweep to consider it seen,
  // so re-stamping it every few seconds would be writes for nothing. A reply
  // arriving while the thread sits open is covered by the next mount — and by
  // the threshold, which is hours.
  //
  // Best-effort: failing to record a read costs at worst one extra reminder,
  // which is not worth an error in front of someone trying to read a ticket.
  useEffect(() => {
    if (!token || !id) return;
    const mark = mode === "agent" ? api.markSupportTicketRead : api.markMyTicketRead;
    mark(token, id).catch(() => {
      /* ignore — the reminder is a nudge, not a guarantee */
    });
  }, [token, id, mode]);

  // Poll while the thread is open so a reply from the other party appears
  // without a manual reload. Silent (no spinner) — load() only sets error on a
  // hard failure. Draft state is separate, so a poll never clobbers what the
  // user is typing.
  useEffect(() => {
    const iv = window.setInterval(() => void load(), POLL.watched);
    return () => window.clearInterval(iv);
  }, [load]);

  useEffect(() => {
    endRef.current?.scrollIntoView({ block: "end" });
  }, [view?.messages.length]);

  // "Did they see my answer?" — shown to the agent, on the newest support
  // message only.
  //
  // Only the newest, because the receipt is per THREAD, not per message: it
  // records when the customer last opened the ticket. Every support message
  // older than it has been read too, but tagging them all turns a signal into
  // wallpaper, and the one that decides what an agent does next is the last.
  //
  // Agent side only, deliberately. The mirror of this — telling the customer
  // support has opened their ticket and not replied — is a stopwatch on the
  // desk rather than an answer to a question, so the customer's view strips
  // that timestamp entirely (ticketForUser in daemon/ticket_routes.go).
  const receiptOn =
    mode === "agent"
      ? [...(view?.messages ?? [])]
          .reverse()
          .find((m) => m.author_kind === "support")?.id
      : undefined;
  // A ticket with NO receipt at all tells us nothing — it predates read
  // tracking, or was filed through the API rather than the UI. Saying "not
  // read yet" there would be a confident guess, so the indicator is absent
  // instead: no badge means no information, not bad news.
  const customerRead: Receipt | undefined = (() => {
    if (!receiptOn || !view?.ticket.user_read_at) return undefined;
    const msg = view.messages.find((m) => m.id === receiptOn);
    if (!msg) return undefined;
    const readAt = new Date(view.ticket.user_read_at);
    if (Number.isNaN(readAt.getTime()) || readAt.getTime() === 0) return undefined;
    return readAt >= new Date(msg.created_at)
      ? { read: true, at: view.ticket.user_read_at }
      : { read: false };
  })();

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

  // The requester's own status control. They can withdraw a ticket or reopen a
  // finished one; only support can declare it resolved (the server enforces it).
  const setMyStatus = async (status: "closed" | "awaiting_support") => {
    if (!token) return;
    setBusy(true);
    try {
      setView(await api.setMyTicketStatus(token, id, status));
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  };

  // Claim ("me") or release ("") the ticket. Assignment is internal to the
  // support side — the customer's view never shows it.
  const assign = async (assignee: string) => {
    if (!token) return;
    setBusy(true);
    try {
      setView(await api.assignSupportTicket(token, id, assignee));
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  };

  const backTo = mode === "agent" ? "/support/queue" : "/support";
  // Name the parent, per the BackLink convention — which parent depends on
  // where this thread was opened from.
  const backLabel = mode === "agent" ? t("support.queueTitle") : t("nav.support");

  if (loading) return <Loading />;
  if (error && !view) {
    return (
      <div>
        <BackLink to={backTo} label={backLabel} />
        <ErrorNotice style={{ marginTop: "var(--space-3)" }}>{error}</ErrorNotice>
      </div>
    );
  }
  if (!view) return null;
  const tk = view.ticket;
  const closed = tk.status === "resolved" || tk.status === "closed";
  // Ownership only exists on the support side; the user surface never receives
  // assigned_to (the server strips it), so `owner` is always "" in user mode.
  const owner = tk.assigned_to ?? "";
  const mine = owner !== "" && owner === (me?.subject ?? "");

  return (
    <div style={{ maxWidth: 760 }}>
      <BackLink to={backTo} label={backLabel} />
      <div className="page-title" style={{ marginTop: "var(--space-2)" }}>
        <div>
          <h1 style={{ fontSize: "var(--text-xl)" }}>{tk.subject}</h1>
          <div className="sub">
            <span className="count-pill" style={{ marginRight: "var(--space-2)" }}>{statusLabel(tk.status)}</span>
            {mode === "agent" && (
              <>
                {tk.tenant}
                {" · "}
                {owner === ""
                  ? t("support.unassigned")
                  : mine
                    ? t("support.assignedToYou")
                    : t("support.assignedTo", { agent: owner })}
                {" · "}
              </>
            )}
            {tk.flow_id && (
              // The customer's own flow lives at /flows/<id>; an agent is in a
              // DIFFERENT tenant, where that route resolves to nothing. Their
              // route into someone else's flow is the grant-gated support view.
              <Link
                to={
                  mode === "agent"
                    ? `/support/flows/${encodeURIComponent(tk.tenant)}/${encodeURIComponent(
                        tk.workspace,
                      )}/${encodeURIComponent(tk.flow_id)}`
                    : `/flows/${encodeURIComponent(tk.flow_id)}`
                }
              >
                {t("support.viewFlow")}
              </Link>
            )}
            {tk.bundle_id && (
              <>
                {" · "}
                <button type="button" className="linklike" onClick={() => setShowBundle(true)}>
                  {t("support.viewBundle")}
                </button>
              </>
            )}
            {/* Tell the agent WHY there's nothing to open, so an unattached
                ticket reads as "ask them which flow" rather than a broken page. */}
            {mode === "agent" && !tk.bundle_id && (
              <>
                {/* The agent block above already ends in a separator, so only
                    add one when the flow link sits between them. */}
                {tk.flow_id && " · "}
                <span className="muted">{t("support.noBundle")}</span>
              </>
            )}
          </div>
        </div>
        <div className="user-card-actions">
          {mode === "agent" && owner === "" && (
            <Button onClick={() => void assign("me")} disabled={busy}>
              <UserCheck size={ICON.xs} />
              {t("support.claim")}
            </Button>
          )}
          {mode === "agent" && mine && (
            <Button onClick={() => void assign("")} disabled={busy}>
              <UserMinus size={ICON.xs} />
              {t("support.release")}
            </Button>
          )}
          {mode === "agent" && !closed && (
            <Button onClick={() => void setStatus("resolved")} disabled={busy}>
              <Check size={ICON.xs} />
              {t("support.resolve")}
            </Button>
          )}
          {/* The requester's own exit: "I don't need this any more". Replying
              reopens it, so there's no separate Reopen button. */}
          {mode === "user" && !closed && (
            <Button onClick={() => setConfirmClose(true)} disabled={busy}>
              <X size={ICON.xs} />
              {t("support.close")}
            </Button>
          )}
        </div>
      </div>

      <div className="ticket-thread">
        {view.messages.map((m) => (
          <ChatBubble
            key={m.id}
            m={m}
            mode={mode}
            receipt={m.id === receiptOn ? customerRead : undefined}
          />
        ))}
        <div ref={endRef} />
      </div>

      {error && <ErrorNotice style={{ marginTop: "var(--space-2)" }}>{error}</ErrorNotice>}

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
          <Send size={ICON.sm} />
          {t("support.send")}
        </Button>
      </div>

      {showBundle && (
        <BundleModal ticketId={id} mode={mode} onClose={() => setShowBundle(false)} />
      )}

      {/* Not `danger`: closing is the requester's own tidy-up and a reply
          undoes it, so it should read as deliberate rather than alarming.
          The body says so, which is the part that turns a confirm from an
          obstacle into an answer to "wait, what happens if I do?". */}
      {confirmClose && (
        <ConfirmModal
          title={t("support.confirmCloseTitle")}
          message={t("support.confirmCloseBody")}
          confirmLabel={t("support.close")}
          onConfirm={() => {
            setConfirmClose(false);
            void setMyStatus("closed");
          }}
          onCancel={() => setConfirmClose(false)}
        />
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


  useEscapeToClose(onClose);
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal"
        onClick={(e) => e.stopPropagation()}
        aria-modal="true"
        role="dialog"
        aria-label={t("bundle.title")}
        style={{ maxWidth: 680 }}
      >
        <div className="modal-head">
          <strong>{t("bundle.title")}</strong>
          <Button size="icon" onClick={onClose} aria-label={t("common.close")}>
            <X size={ICON.md} />
          </Button>
        </div>
        <div className="modal-body">
          {error && <ErrorNotice>{error}</ErrorNotice>}
          {!bundle && !error && <Loading inline />}
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
// SYSTEM_NOTE maps the daemon's system_code to an i18n key. Mirrors the
// SystemNote constants in core/ticket.go; the daemon composes the same notes in
// English into `body`, which is what an API reader or an email digest gets and
// what this falls back to for a code it does not know — an older row, or a
// newer daemon than this build.
//
// Two of them have a "you" form. The customer's own close and reopen are the
// only notes about something the reader did, and "The customer closed this
// ticket" in your own thread is the third person talking about you — the same
// complaint as an email address where your name should be.
const SYSTEM_NOTE: Record<string, string> = {
  customer_closed: "support.note.customerClosed",
  customer_reopened: "support.note.customerReopened",
  grant_requested: "support.note.grantRequested",
  marked_open: "support.note.markedOpen",
  marked_awaiting_user: "support.note.markedAwaitingUser",
  marked_awaiting_support: "support.note.markedAwaitingSupport",
  marked_resolved: "support.note.markedResolved",
  marked_closed: "support.note.markedClosed",
};
const SYSTEM_NOTE_YOURS: Record<string, string> = {
  customer_closed: "support.note.customerClosedYou",
  customer_reopened: "support.note.customerReopenedYou",
};

// Receipt is what the agent is told about their newest reply: read, with when,
// or not read yet. Absent means "we do not know" — see customerRead above.
type Receipt = { read: true; at: string } | { read: false };

function ChatBubble({
  m,
  mode,
  receipt,
}: {
  m: TicketMessage;
  mode: "user" | "agent";
  receipt?: Receipt;
}) {
  const { t } = useTranslation();
  const { me } = useAuth();
  if (m.author_kind === "system") {
    // The customer is the reader on the user surface, so their own actions are
    // narrated in the second person there and the third person to an agent.
    const key =
      (mode === "user" ? SYSTEM_NOTE_YOURS[m.system_code ?? ""] : undefined) ??
      SYSTEM_NOTE[m.system_code ?? ""];
    return (
      <div className="ticket-system-note">{key ? t(key) : m.body}</div>
    );
  }
  // Which SIDE the bubble sits on is about the party, not the person: a
  // colleague's reply belongs on the support side of an agent's screen.
  const mine = mode === "agent" ? m.author_kind === "support" : m.author_kind === "user";
  // Who it is FROM is about the person. "support.fromYou" was already here and
  // already meant this, but it only applied when the server sent no author —
  // which it always does, so every one of your own messages was headed with
  // your own email address back at you.
  //
  // Matched on identity rather than on `mine`, because the two are not the
  // same thing on the agent side: every support reply is "mine" for
  // alignment, and labelling a colleague's "You" would be a lie about who
  // said it. The customer still never learns which agent replied — a support
  // message that isn't yours falls through to the generic "Support", which is
  // the same thing their view showed before.
  const who =
    m.author && me?.subject && m.author === me.subject
      ? t("support.fromYou")
      : m.author_kind === "support"
        ? t("support.fromSupport")
        : m.author || t("support.fromYou");
  return (
    <div className={"ticket-bubble" + (mine ? " mine" : "")}>
      <div className="ticket-bubble-meta">{who} · {formatDateTime(m.created_at)}</div>
      <div className="ticket-bubble-body">{m.body}</div>
      {receipt && (
        <div className="ticket-bubble-receipt">
          {receipt.read
            ? t("support.readByCustomer", { time: formatDateTime(receipt.at) })
            : t("support.notReadYet")}
        </div>
      )}
    </div>
  );
}
