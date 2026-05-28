import { useState, type ReactNode } from "react";
import { AlertCircle, Check, Copy, Terminal } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import type { IssuedAPIKey } from "../types";

// ConnectMcpClientModal mints an API key scoped to the current
// principal, then hands the user the right config snippet for their
// MCP client. The modal hides the admin framing of /admin/api-keys —
// it picks role + workspace defaults that make sense for an LLM
// agent and surfaces only the bits the user actually needs to paste.
//
// New clients are added by extending CLIENTS below. Each entry owns
// its label, icon, and snippet builders so the renderer stays a flat
// switch over the active client. Both clients we ship today share the
// Anthropic Claude brand; the card icons differentiate Desktop (raw
// mark) from Code (mark inside a terminal frame).

const CLAUDE_ROLE = {
  name: "claude-mcp",
  permissions: ["graph:run", "graph:edit"] as const,
};

type Stage = "confirm" | "reveal";

type ClientID = "claude-desktop" | "claude-code";

type ClientDef = {
  id: ClientID;
  labelKey: string;
  vendorKey: string;
  Icon: () => ReactNode;
  instructionsKey: string;
  configPathKey: string;
  buildJSON: (env: SnippetEnv) => string;
  // Optional CLI install command — Claude Code has one; Claude
  // Desktop doesn't and the section is hidden when buildCLI is unset.
  buildCLI?: (env: SnippetEnv) => string;
};

type SnippetEnv = { url: string; secret: string };

const CLIENTS: ClientDef[] = [
  {
    id: "claude-desktop",
    labelKey: "connectMcp.clients.claudeDesktop.label",
    vendorKey: "connectMcp.clients.anthropic",
    Icon: ClaudeLogo,
    instructionsKey: "connectMcp.clients.claudeDesktop.instructions",
    configPathKey: "connectMcp.clients.claudeDesktop.configPath",
    buildJSON: mcpServersJSON,
  },
  {
    id: "claude-code",
    labelKey: "connectMcp.clients.claudeCode.label",
    vendorKey: "connectMcp.clients.anthropic",
    Icon: ClaudeCodeLogo,
    instructionsKey: "connectMcp.clients.claudeCode.instructions",
    configPathKey: "connectMcp.clients.claudeCode.configPath",
    buildJSON: mcpServersJSON,
    buildCLI: ({ url, secret }) =>
      // Trailing `--` separates `claude mcp add` flags from the
      // command + args that Claude Code will spawn. Multi-line with
      // backslashes for readability when the URL is long.
      `claude mcp add hazy-flow \\\n  --env HAZYFLOW_URL=${shellQuote(url)} \\\n  --env HAZYFLOW_API_KEY=${shellQuote(secret)} \\\n  -- hz-mcp`,
  },
];

export function ConnectMcpClientModal({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const { token, me, activeWorkspace } = useAuth();
  const [stage, setStage] = useState<Stage>("confirm");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [issued, setIssued] = useState<IssuedAPIKey | null>(null);

  const subject = me?.subject ?? "";
  const workspace = activeWorkspace || me?.workspace || "";

  const create = async () => {
    if (!token) return;
    if (!subject) {
      setError(t("connectMcp.noSubject"));
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      // Self-issue path. Server takes subject/tenant/workspace from
      // the session and caps permissions to a subset of the caller's
      // own — so the Connect button works for any signed-in member,
      // not just tenant admins.
      const k = await api.issueMyAPIKey(token, {
        roles: [
          {
            name: CLAUDE_ROLE.name,
            permissions: [...CLAUDE_ROLE.permissions],
          },
        ],
      });
      setIssued(k);
      setStage("reveal");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div
      className="settings-backdrop"
      onClick={stage === "confirm" ? onClose : undefined}
    >
      <div
        className="settings-dialog"
        style={{ maxWidth: 680 }}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="settings-head">
          <h2>{t("connectMcp.title")}</h2>
        </div>
        {stage === "confirm" ? (
          <ConfirmStage
            subject={subject}
            workspace={workspace}
            submitting={submitting}
            error={error}
            onCancel={onClose}
            onCreate={create}
          />
        ) : (
          <RevealStage issued={issued!} onDone={onClose} />
        )}
      </div>
    </div>
  );
}

function ConfirmStage({
  subject,
  workspace,
  submitting,
  error,
  onCancel,
  onCreate,
}: {
  subject: string;
  workspace: string;
  submitting: boolean;
  error: string | null;
  onCancel: () => void;
  onCreate: () => void;
}) {
  const { t } = useTranslation();
  return (
    <>
      <div className="settings-body">
        <p className="settings-help">{t("connectMcp.intro")}</p>
        <div className="sf-field">
          <div className="label-row">
            <label>{t("connectMcp.subjectLabel")}</label>
          </div>
          <input value={subject} disabled style={{ fontFamily: "var(--font-mono)" }} />
        </div>
        <div className="sf-field">
          <div className="label-row">
            <label>{t("connectMcp.workspaceLabel")}</label>
          </div>
          <input
            value={workspace || t("connectMcp.workspaceAny")}
            disabled
            style={{ fontFamily: "var(--font-mono)" }}
          />
          <div className="desc">{t("connectMcp.scopeDesc")}</div>
        </div>
        {error && (
          <div className="card" style={{ color: "var(--danger)", marginTop: "var(--space-3)" }}>
            <AlertCircle size={14} style={{ marginRight: 6, verticalAlign: -2 }} />
            {error}
          </div>
        )}
      </div>
      <div className="settings-foot">
        <button type="button" onClick={onCancel} disabled={submitting}>
          {t("connectMcp.cancel")}
        </button>
        <button type="button" className="primary" onClick={onCreate} disabled={submitting}>
          {submitting ? t("connectMcp.creating") : t("connectMcp.create")}
        </button>
      </div>
    </>
  );
}

function RevealStage({ issued, onDone }: { issued: IssuedAPIKey; onDone: () => void }) {
  const { t } = useTranslation();
  const [activeID, setActiveID] = useState<ClientID>(CLIENTS[0].id);
  const env: SnippetEnv = {
    url: window.location.origin,
    secret: issued.secret,
  };
  const active = CLIENTS.find((c) => c.id === activeID) ?? CLIENTS[0];
  const json = active.buildJSON(env);
  const cli = active.buildCLI?.(env);
  return (
    <>
      <div className="settings-body">
        <p className="settings-help">{t("connectMcp.revealHelp")}</p>
        <div className="mcp-client-grid">
          {CLIENTS.map((c) => (
            <button
              key={c.id}
              type="button"
              className={"mcp-client-card" + (c.id === activeID ? " active" : "")}
              onClick={() => setActiveID(c.id)}
            >
              <span className="mcp-client-icon">
                <c.Icon />
              </span>
              <span className="mcp-client-meta">
                <span className="mcp-client-name">{t(c.labelKey)}</span>
                <span className="mcp-client-vendor">{t(c.vendorKey)}</span>
              </span>
            </button>
          ))}
        </div>
        <div className="mcp-client-instructions">
          <p>{t(active.instructionsKey)}</p>
          <div className="sf-field">
            <div className="label-row">
              <label>{t("connectMcp.configPathLabel")}</label>
            </div>
            <input
              value={t(active.configPathKey)}
              readOnly
              onFocus={(e) => e.currentTarget.select()}
              style={{ fontFamily: "var(--font-mono)" }}
            />
          </div>
          <div className="sf-field">
            <div className="label-row">
              <label>{t("connectMcp.snippetLabel")}</label>
            </div>
            <pre className="secret-reveal" style={{ whiteSpace: "pre", overflowX: "auto" }}>
              {json}
            </pre>
            <CopyButton text={json} labelKey="connectMcp.copySnippet" />
          </div>
          {cli && (
            <div className="sf-field">
              <div className="label-row">
                <label>
                  <Terminal size={12} style={{ marginRight: 6, verticalAlign: -1 }} />
                  {t("connectMcp.cliLabel")}
                </label>
              </div>
              <pre className="secret-reveal" style={{ whiteSpace: "pre", overflowX: "auto" }}>
                {cli}
              </pre>
              <CopyButton text={cli} labelKey="connectMcp.copyCli" />
            </div>
          )}
        </div>
        <details style={{ marginTop: "var(--space-3)" }}>
          <summary style={{ cursor: "pointer", color: "var(--muted)" }}>
            {t("connectMcp.advancedSummary")}
          </summary>
          <div className="sf-field" style={{ marginTop: "var(--space-2)" }}>
            <div className="label-row">
              <label>{t("connectMcp.rawTokenLabel")}</label>
            </div>
            <input
              value={issued.secret}
              readOnly
              onFocus={(e) => e.currentTarget.select()}
              style={{ fontFamily: "var(--font-mono)" }}
            />
            <CopyButton text={issued.secret} labelKey="connectMcp.copyToken" />
          </div>
        </details>
      </div>
      <div className="settings-foot">
        <button type="button" className="primary" onClick={onDone}>
          {t("connectMcp.done")}
        </button>
      </div>
    </>
  );
}

function CopyButton({ text, labelKey }: { text: string; labelKey: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      /* clipboard blocked — input/pre is selectable for manual copy */
    }
  };
  return (
    <button type="button" onClick={copy} style={{ marginTop: "var(--space-2)" }}>
      {copied ? (
        <Check size={12} style={{ marginRight: 6, verticalAlign: -1 }} />
      ) : (
        <Copy size={12} style={{ marginRight: 6, verticalAlign: -1 }} />
      )}
      {copied ? t("connectMcp.copied") : t(labelKey)}
    </button>
  );
}

function mcpServersJSON({ url, secret }: SnippetEnv): string {
  const config = {
    mcpServers: {
      "hazy-flow": {
        command: "hz-mcp",
        env: {
          HAZYFLOW_URL: url,
          HAZYFLOW_API_KEY: secret,
        },
      },
    },
  };
  return JSON.stringify(config, null, 2);
}

// shellQuote wraps a value for safe paste into a POSIX shell. Single
// quotes suppress all expansion; embedded single quotes get the
// standard '"'"' dance.
function shellQuote(v: string): string {
  return "'" + v.replace(/'/g, `'"'"'`) + "'";
}

// ClaudeLogo is the Anthropic Claude mark — a stylized asterisk /
// sunburst. currentColor lets the card's active/hover states tint it.
function ClaudeLogo() {
  return (
    <svg
      width="24"
      height="24"
      viewBox="0 0 24 24"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      aria-hidden="true"
    >
      <path
        d="M12 2 L13.4 9.3 L20.5 7.4 L15.1 12 L20.5 16.6 L13.4 14.7 L12 22 L10.6 14.7 L3.5 16.6 L8.9 12 L3.5 7.4 L10.6 9.3 Z"
        fill="currentColor"
      />
    </svg>
  );
}

// ClaudeCodeLogo wraps the Claude mark cue (chevron + line) inside a
// terminal frame so the two Claude products read as distinct cards.
function ClaudeCodeLogo() {
  return (
    <svg
      width="24"
      height="24"
      viewBox="0 0 24 24"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      aria-hidden="true"
    >
      <rect
        x="2.5"
        y="4.5"
        width="19"
        height="15"
        rx="2"
        stroke="currentColor"
        strokeWidth="1.5"
        fill="none"
      />
      <path
        d="M6 9 L9 12 L6 15"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      <path
        d="M11 15 L16 15"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinecap="round"
      />
    </svg>
  );
}
