import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { Send, Sparkles, Square, X, Wrench, CheckCircle2, AlertCircle } from "lucide-react";
import { useTranslation } from "react-i18next";
import i18n from "../i18n";
import { api } from "../api";
import { useAuth } from "../auth";
import type { Graph } from "../types";

// One entry the user can see in the transcript. UserMessage is a
// plain prompt; AssistantMessage carries the streamed text + an
// ordered list of inline events (tool uses, proposals, errors) so
// the transcript renders chronologically.
type UserMessage = { role: "user"; text: string };
type ToolEvent =
  | { kind: "tool"; id: string; name: string; status: "running" | "done" | "error"; resultPreview?: string }
  | { kind: "proposal"; id: string; graph: Graph; status: "pending" | "applied" | "discarded" | "applying"; autoApplied?: boolean; error?: string }
  | { kind: "error"; message: string; code?: string };
type AssistantMessage = {
  role: "assistant";
  text: string;
  events: ToolEvent[];
  done: boolean;
};
type Message = UserMessage | AssistantMessage;

type Props = {
  open: boolean;
  onClose: () => void;
  // applyProposal commits a proposed graph through the editor's
  // save path (NOT through a chat-tool save) so the editor's own
  // dirty/locked state stays consistent. Returns the persisted
  // commit hash; the chat panel uses it to flip the proposal card
  // into "applied".
  applyProposal: (g: Graph) => Promise<void>;
};

// ChatPanel is the agentic-AI chat sidebar. It hangs off the right
// side of the editor while open. Messages are kept in component
// state (per-tab ephemeral, by design — full persistence comes
// later).
export function ChatPanel({ open, onClose, applyProposal }: Props) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [messages, setMessages] = useState<Message[]>([]);
  const [input, setInput] = useState("");
  const [streaming, setStreaming] = useState(false);
  const abortRef = useRef<AbortController | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);

  // Auto-scroll on every new event so the user sees the latest
  // delta without having to drag.
  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [messages]);

  if (!open) return null;

  const sendMessage = async () => {
    const text = input.trim();
    if (!text || !token || streaming) return;
    setInput("");

    // Append the user's message AND a fresh empty assistant slot the
    // streaming events will fill. The two go in together so the
    // transcript can't show the user message without an answering
    // bubble.
    const userMsg: UserMessage = { role: "user", text };
    const assistantSlot: AssistantMessage = { role: "assistant", text: "", events: [], done: false };
    setMessages((m) => [...m, userMsg, assistantSlot]);

    // Build the wire payload: every message becomes a {role, content}
    // entry. We send text as a plain string for user messages — the
    // server-side agent accepts either string or content-block array.
    const wire = [
      ...messages.flatMap((m) => (m.role === "user" ? [{ role: "user" as const, content: m.text }] : [])),
      { role: "user" as const, content: text },
    ];

    setStreaming(true);
    const ctrl = new AbortController();
    abortRef.current = ctrl;

    try {
      await api.streamChat(token, wire, (kind, data) => {
        // Append to the LATEST assistant message — index from the
        // end so concurrent sends don't fight over indices.
        setMessages((m) => {
          const last = m[m.length - 1];
          if (!last || last.role !== "assistant") return m;
          const updated: AssistantMessage = { ...last, events: [...last.events] };
          switch (kind) {
            case "text":
              updated.text += (data?.delta as string) ?? "";
              break;
            case "tool_use_start":
              updated.events.push({
                kind: "tool",
                id: data?.tool_id as string,
                name: data?.tool ?? "tool",
                status: "running",
              });
              break;
            case "tool_use_result": {
              const idx = updated.events.findIndex(
                (e) => e.kind === "tool" && e.id === data?.tool_id,
              );
              const tev = idx >= 0 ? updated.events[idx] : undefined;
              if (tev && tev.kind === "tool") {
                updated.events[idx] = {
                  kind: "tool",
                  id: tev.id,
                  name: tev.name,
                  status: data?.error_text ? "error" : "done",
                  resultPreview: data?.error_text || truncate(data?.result_text as string),
                };
              }
              break;
            }
            case "proposal":
              updated.events.push({
                kind: "proposal",
                // Proposal IDs are unique per event so React keys are
                // stable even when the agent proposes more than once.
                id: `${data?.proposal?.id}-${updated.events.length}`,
                graph: data?.proposal as Graph,
                // auto-applied means the agent already saved via its
                // own tools; the card shows as done and the UI skips
                // the Apply button.
                status: data?.auto_applied ? "applied" : "pending",
                autoApplied: !!data?.auto_applied,
              });
              break;
            case "error":
              updated.events.push({
                kind: "error",
                message: data?.error_text ?? data?.error ?? i18n.t("chatPanel.unknownError"),
                code: typeof data?.error_code === "string" ? data.error_code : undefined,
              });
              updated.done = true;
              break;
            case "done":
              updated.done = true;
              break;
          }
          return [...m.slice(0, -1), updated];
        });
      }, ctrl.signal);
    } catch (e: unknown) {
      const msg = (e as Error).message;
      setMessages((m) => {
        const last = m[m.length - 1];
        if (!last || last.role !== "assistant") return m;
        const updated: AssistantMessage = {
          ...last,
          done: true,
          events: [...last.events, { kind: "error", message: msg }],
        };
        return [...m.slice(0, -1), updated];
      });
    } finally {
      setStreaming(false);
      abortRef.current = null;
    }
  };

  const stop = () => {
    abortRef.current?.abort();
  };

  // applyAt flips the indexed proposal through "applying" → "applied"
  // (or back to "pending" on failure). We resolve the graph from the
  // current state snapshot BEFORE the optimistic setState so
  // TypeScript can narrow it without an extra assertion.
  const applyAt = async (msgIdx: number, propID: string) => {
    const msg = messages[msgIdx];
    if (!msg || msg.role !== "assistant") return;
    const prop = msg.events.find(
      (e): e is Extract<ToolEvent, { kind: "proposal" }> =>
        e.kind === "proposal" && e.id === propID,
    );
    if (!prop) return;
    setMessages((m) => updateProposal(m, msgIdx, propID, { status: "applying", error: undefined }));
    try {
      await applyProposal(prop.graph);
      setMessages((m) => updateProposal(m, msgIdx, propID, { status: "applied" }));
    } catch (e) {
      setMessages((m) =>
        updateProposal(m, msgIdx, propID, {
          status: "pending",
          error: (e as Error).message,
        }),
      );
    }
  };

  const discardAt = (msgIdx: number, propID: string) => {
    setMessages((m) => updateProposal(m, msgIdx, propID, { status: "discarded" }));
  };

  // EXAMPLE_PROMPTS feeds three one-click chips on the empty state. A
  // non-tech user doesn't know what level of detail the agent expects;
  // seeing concrete first-person prompts ("Ping me on Slack…") makes
  // the field discoverable. The keys live under chatPanel.example*
  // so the copy localises.
  const EXAMPLE_PROMPTS: { labelKey: string }[] = [
    { labelKey: "chatPanel.exampleDailyReport" },
    { labelKey: "chatPanel.exampleNewLead" },
    { labelKey: "chatPanel.exampleInvoiceLog" },
  ];

  return (
    <aside className="chat-panel">
      <header className="chat-head">
        <Sparkles size={14} />
        <span style={{ flex: 1 }}>{t("chatPanel.title")}</span>
        <button className="icon ghost" onClick={onClose} aria-label={t("chatPanel.close")}>
          <X size={14} />
        </button>
      </header>
      <div className="chat-scroll" ref={scrollRef}>
        {messages.length === 0 && (
          <div className="chat-empty">
            <Sparkles size={20} />
            <p>{t("chatPanel.emptyP1")}</p>
            <p style={{ fontSize: 12, color: "var(--faint)" }}>
              {t("chatPanel.emptyP2")}
            </p>
            <div className="chat-empty-examples">
              {EXAMPLE_PROMPTS.map((ex) => (
                <button
                  key={ex.labelKey}
                  type="button"
                  className="chat-example-chip"
                  onClick={() => setInput(t(ex.labelKey))}
                  disabled={streaming}
                >
                  {t(ex.labelKey)}
                </button>
              ))}
            </div>
          </div>
        )}
        {messages.map((m, i) => (
          <ChatMessageView
            key={i}
            msg={m}
            onApply={(propID) => applyAt(i, propID)}
            onDiscard={(propID) => discardAt(i, propID)}
          />
        ))}
        {streaming && (
          <div className="chat-meta">
            <span className="dot pulse" /> {t("chatPanel.thinking")}
          </div>
        )}
      </div>
      <form
        className="chat-input"
        onSubmit={(e) => {
          e.preventDefault();
          void sendMessage();
        }}
      >
        <textarea
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void sendMessage();
            }
          }}
          placeholder={streaming ? t("chatPanel.placeholderStreaming") : t("chatPanel.placeholderIdle")}
          rows={2}
          disabled={streaming}
        />
        {streaming ? (
          <button type="button" className="ghost" onClick={stop} title={t("chatPanel.stop")}>
            <Square size={14} />
          </button>
        ) : (
          <button type="submit" className="primary" disabled={!input.trim() || !token}>
            <Send size={14} />
          </button>
        )}
      </form>
    </aside>
  );
}

// humanTool maps the agent's raw tool names to plain-English labels
// so a non-technical user reading the chat transcript doesn't see
// "save_graph" or "list_drops" — both internal identifiers that mean
// nothing outside this codebase. Unknown tools fall back to a generic
// "Working: <name>" so a future tool surfaces *something* readable
// even if we forget to add a mapping.
function humanTool(
  name: string,
  t: (key: string, opts?: Record<string, unknown>) => string,
): string {
  switch (name) {
    case "list_drops":
      return t("chatPanel.toolListDrops");
    case "list_flows":
      return t("chatPanel.toolListFlows");
    case "get_flow":
      return t("chatPanel.toolGetFlow");
    case "propose_flow":
      return t("chatPanel.toolProposeFlow");
    default:
      return t("chatPanel.toolGeneric", { name });
  }
}

function ChatMessageView({
  msg,
  onApply,
  onDiscard,
}: {
  msg: Message;
  onApply: (propID: string) => void;
  onDiscard: (propID: string) => void;
}) {
  const { t } = useTranslation();
  if (msg.role === "user") {
    return <div className="chat-msg user">{msg.text}</div>;
  }
  return (
    <div className="chat-msg assistant">
      {msg.text && <div className="chat-text">{msg.text}</div>}
      {msg.events.map((e, i) => {
        if (e.kind === "tool") {
          return (
            <div key={i} className={`chat-tool ${e.status}`}>
              {e.status === "running" && <Wrench size={12} className="spin" />}
              {e.status === "done" && <CheckCircle2 size={12} />}
              {e.status === "error" && <AlertCircle size={12} />}
              <span className="chat-tool-name" title={e.name}>
                {humanTool(e.name, t)}
              </span>
              {e.status === "running" && <span style={{ color: "var(--faint)" }}>…</span>}
            </div>
          );
        }
        if (e.kind === "proposal") {
          const headLabel = e.autoApplied
            ? t("chatPanel.savedFlow")
            : t("chatPanel.proposedFlow");
          // Prefer the human flow name; fall back to the slug. The
          // slug stays in the title attr so anyone debugging can
          // still see the underlying ID on hover.
          const displayName = e.graph.name?.trim() || e.graph.id;
          return (
            <div key={i} className={`chat-proposal ${e.status}`}>
              <div className="chat-proposal-head">
                <Sparkles size={14} />
                <span>
                  {headLabel}: <strong title={e.graph.id}>{displayName}</strong>
                </span>
              </div>
              <div className="chat-proposal-summary">
                {t("chatPanel.nodesEdges", {
                  nodes: e.graph.nodes?.length ?? 0,
                  edges: e.graph.edges?.length ?? 0,
                })}
              </div>
              {e.error && (
                <div className="chat-error">{e.error}</div>
              )}
              {e.status === "pending" && !e.autoApplied && (
                <div className="chat-proposal-actions">
                  <button className="primary" onClick={() => onApply(e.id)}>
                    {t("chatPanel.apply")}
                  </button>
                  <button className="ghost" onClick={() => onDiscard(e.id)}>
                    {t("chatPanel.discard")}
                  </button>
                </div>
              )}
              {e.status === "applying" && <div className="chat-meta">{t("chatPanel.applying")}</div>}
              {e.status === "applied" && (
                // No manual "Reload canvas" button: the editor reseeds
                // its state in place once applyProposal returns, so the
                // canvas already reflects the new graph by the time
                // this status flip lands. The success line stands alone.
                <div className="chat-meta success">
                  {e.autoApplied ? t("chatPanel.savedByAgent") : t("chatPanel.applied")}
                </div>
              )}
              {e.status === "discarded" && <div className="chat-meta">{t("chatPanel.discarded")}</div>}
            </div>
          );
        }
        return (
          <div key={i} className="chat-error">
            <AlertCircle size={12} /> {e.message}
            {e.code === "anthropic_key_missing" && (
              <div style={{ marginTop: 6 }}>
                <Link to="/admin/chat">
                  {t("chatPanel.openChatSettings")}
                </Link>
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

function updateProposal(
  msgs: Message[],
  idx: number,
  propID: string,
  patch: Partial<Extract<ToolEvent, { kind: "proposal" }>>,
): Message[] {
  const copy = [...msgs];
  const m = copy[idx];
  if (!m || m.role !== "assistant") return msgs;
  copy[idx] = {
    ...m,
    events: m.events.map((e) =>
      e.kind === "proposal" && e.id === propID ? { ...e, ...patch } : e,
    ),
  };
  return copy;
}

function truncate(s: string | undefined): string | undefined {
  if (!s) return s;
  return s.length > 200 ? s.slice(0, 200) + "…" : s;
}
