import { jsx as _jsx, jsxs as _jsxs } from "react/jsx-runtime";
import { useEffect, useRef, useState } from "react";
import { Send, Sparkles, Square, X, Wrench, CheckCircle2, AlertCircle } from "lucide-react";
import { useTranslation } from "react-i18next";
import i18n from "../i18n";
import { api } from "../api";
import { useAuth } from "../auth";
// ChatPanel is the agentic-AI chat sidebar. It hangs off the right
// side of the editor while open. Messages are kept in component
// state (per-tab ephemeral, by design — full persistence comes
// later).
export function ChatPanel({ open, onClose, applyProposal }) {
    const { t } = useTranslation();
    const { token } = useAuth();
    const [messages, setMessages] = useState([]);
    const [input, setInput] = useState("");
    const [streaming, setStreaming] = useState(false);
    const abortRef = useRef(null);
    const scrollRef = useRef(null);
    // Auto-scroll on every new event so the user sees the latest
    // delta without having to drag.
    useEffect(() => {
        if (scrollRef.current) {
            scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
        }
    }, [messages]);
    if (!open)
        return null;
    const sendMessage = async () => {
        const text = input.trim();
        if (!text || !token || streaming)
            return;
        setInput("");
        // Append the user's message AND a fresh empty assistant slot the
        // streaming events will fill. The two go in together so the
        // transcript can't show the user message without an answering
        // bubble.
        const userMsg = { role: "user", text };
        const assistantSlot = { role: "assistant", text: "", events: [], done: false };
        setMessages((m) => [...m, userMsg, assistantSlot]);
        // Build the wire payload: every message becomes a {role, content}
        // entry. We send text as a plain string for user messages — the
        // server-side agent accepts either string or content-block array.
        const wire = [
            ...messages.flatMap((m) => (m.role === "user" ? [{ role: "user", content: m.text }] : [])),
            { role: "user", content: text },
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
                    if (!last || last.role !== "assistant")
                        return m;
                    const updated = { ...last, events: [...last.events] };
                    switch (kind) {
                        case "text":
                            updated.text += data?.delta ?? "";
                            break;
                        case "tool_use_start":
                            updated.events.push({
                                kind: "tool",
                                id: data?.tool_id,
                                name: data?.tool ?? "tool",
                                status: "running",
                            });
                            break;
                        case "tool_use_result": {
                            const idx = updated.events.findIndex((e) => e.kind === "tool" && e.id === data?.tool_id);
                            const tev = idx >= 0 ? updated.events[idx] : undefined;
                            if (tev && tev.kind === "tool") {
                                updated.events[idx] = {
                                    kind: "tool",
                                    id: tev.id,
                                    name: tev.name,
                                    status: data?.error_text ? "error" : "done",
                                    resultPreview: data?.error_text || truncate(data?.result_text),
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
                                graph: data?.proposal,
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
        }
        catch (e) {
            const msg = e.message;
            setMessages((m) => {
                const last = m[m.length - 1];
                if (!last || last.role !== "assistant")
                    return m;
                const updated = {
                    ...last,
                    done: true,
                    events: [...last.events, { kind: "error", message: msg }],
                };
                return [...m.slice(0, -1), updated];
            });
        }
        finally {
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
    const applyAt = async (msgIdx, propID) => {
        const msg = messages[msgIdx];
        if (!msg || msg.role !== "assistant")
            return;
        const prop = msg.events.find((e) => e.kind === "proposal" && e.id === propID);
        if (!prop)
            return;
        setMessages((m) => updateProposal(m, msgIdx, propID, { status: "applying", error: undefined }));
        try {
            await applyProposal(prop.graph);
            setMessages((m) => updateProposal(m, msgIdx, propID, { status: "applied" }));
        }
        catch (e) {
            setMessages((m) => updateProposal(m, msgIdx, propID, {
                status: "pending",
                error: e.message,
            }));
        }
    };
    const discardAt = (msgIdx, propID) => {
        setMessages((m) => updateProposal(m, msgIdx, propID, { status: "discarded" }));
    };
    return (_jsxs("aside", { className: "chat-panel", children: [_jsxs("header", { className: "chat-head", children: [_jsx(Sparkles, { size: 14 }), _jsx("span", { style: { flex: 1 }, children: t("chatPanel.title") }), _jsx("button", { className: "icon ghost", onClick: onClose, "aria-label": t("chatPanel.close"), children: _jsx(X, { size: 14 }) })] }), _jsxs("div", { className: "chat-scroll", ref: scrollRef, children: [messages.length === 0 && (_jsxs("div", { className: "chat-empty", children: [_jsx(Sparkles, { size: 20 }), _jsx("p", { children: t("chatPanel.emptyP1") }), _jsx("p", { style: { fontSize: 12, color: "var(--faint)" }, children: t("chatPanel.emptyP2") })] })), messages.map((m, i) => (_jsx(ChatMessageView, { msg: m, onApply: (propID) => applyAt(i, propID), onDiscard: (propID) => discardAt(i, propID) }, i))), streaming && (_jsxs("div", { className: "chat-meta", children: [_jsx("span", { className: "dot pulse" }), " ", t("chatPanel.thinking")] }))] }), _jsxs("form", { className: "chat-input", onSubmit: (e) => {
                    e.preventDefault();
                    void sendMessage();
                }, children: [_jsx("textarea", { value: input, onChange: (e) => setInput(e.target.value), onKeyDown: (e) => {
                            if (e.key === "Enter" && !e.shiftKey) {
                                e.preventDefault();
                                void sendMessage();
                            }
                        }, placeholder: streaming ? t("chatPanel.placeholderStreaming") : t("chatPanel.placeholderIdle"), rows: 2, disabled: streaming }), streaming ? (_jsx("button", { type: "button", className: "ghost", onClick: stop, title: t("chatPanel.stop"), children: _jsx(Square, { size: 14 }) })) : (_jsx("button", { type: "submit", className: "primary", disabled: !input.trim() || !token, children: _jsx(Send, { size: 14 }) }))] })] }));
}
function ChatMessageView({ msg, onApply, onDiscard, }) {
    const { t } = useTranslation();
    if (msg.role === "user") {
        return _jsx("div", { className: "chat-msg user", children: msg.text });
    }
    return (_jsxs("div", { className: "chat-msg assistant", children: [msg.text && _jsx("div", { className: "chat-text", children: msg.text }), msg.events.map((e, i) => {
                if (e.kind === "tool") {
                    return (_jsxs("div", { className: `chat-tool ${e.status}`, children: [e.status === "running" && _jsx(Wrench, { size: 12, className: "spin" }), e.status === "done" && _jsx(CheckCircle2, { size: 12 }), e.status === "error" && _jsx(AlertCircle, { size: 12 }), _jsx("span", { className: "chat-tool-name", children: e.name }), e.status === "running" && _jsx("span", { style: { color: "var(--faint)" }, children: "\u2026" })] }, i));
                }
                if (e.kind === "proposal") {
                    const headLabel = e.autoApplied
                        ? t("chatPanel.savedFlow")
                        : t("chatPanel.proposedFlow");
                    return (_jsxs("div", { className: `chat-proposal ${e.status}`, children: [_jsxs("div", { className: "chat-proposal-head", children: [_jsx(Sparkles, { size: 14 }), _jsxs("span", { children: [headLabel, ": ", _jsx("code", { children: e.graph.id })] })] }), _jsx("div", { className: "chat-proposal-summary", children: t("chatPanel.nodesEdges", {
                                    nodes: e.graph.nodes?.length ?? 0,
                                    edges: e.graph.edges?.length ?? 0,
                                }) }), e.error && (_jsx("div", { className: "chat-error", children: e.error })), e.status === "pending" && !e.autoApplied && (_jsxs("div", { className: "chat-proposal-actions", children: [_jsx("button", { className: "primary", onClick: () => onApply(e.id), children: t("chatPanel.apply") }), _jsx("button", { className: "ghost", onClick: () => onDiscard(e.id), children: t("chatPanel.discard") })] })), e.status === "applying" && _jsx("div", { className: "chat-meta", children: t("chatPanel.applying") }), e.status === "applied" && (_jsxs("div", { className: "chat-proposal-actions", children: [_jsx("div", { className: "chat-meta success", style: { flex: 1 }, children: e.autoApplied ? t("chatPanel.savedByAgent") : t("chatPanel.applied") }), _jsx("button", { className: "ghost", onClick: () => window.location.reload(), children: t("chatPanel.reloadCanvas") })] })), e.status === "discarded" && _jsx("div", { className: "chat-meta", children: t("chatPanel.discarded") })] }, i));
                }
                return (_jsxs("div", { className: "chat-error", children: [_jsx(AlertCircle, { size: 12 }), " ", e.message] }, i));
            })] }));
}
function updateProposal(msgs, idx, propID, patch) {
    const copy = [...msgs];
    const m = copy[idx];
    if (!m || m.role !== "assistant")
        return msgs;
    copy[idx] = {
        ...m,
        events: m.events.map((e) => e.kind === "proposal" && e.id === propID ? { ...e, ...patch } : e),
    };
    return copy;
}
function truncate(s) {
    if (!s)
        return s;
    return s.length > 200 ? s.slice(0, 200) + "…" : s;
}
