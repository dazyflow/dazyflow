// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { AlertTriangle, Blocks, Pencil, RefreshCw, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "../../components/ui/Button";
import { useAuth } from "../../auth";
import { api } from "../../api";
import type { MCPServer, MCPServerInput } from "../../types";
import { explainApiError } from "../../lib/explainApiError";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { EmptyState } from "../../components/ui/EmptyState";
import { Loading } from "../../components/ui/Loading";
import { Notice } from "../../components/ui/Notice";
import { ICON } from "../../icons";
import { formatDateTime } from "../../lib/datetime";

// AdminMCPServers is where an org points Dazyflow at someone else's tool
// catalog and gets steps out of it.
//
// The page is a form and a list, and the form SAVES BY CONNECTING: pressing
// Save handshakes with the endpoint and the result comes back on the row. That
// is deliberate. The two things that go wrong here — a wrong URL and a token
// the server won't accept — are both invisible until something tries, and an
// admin who has just pasted both is the person best placed to fix them.
export function AdminMCPServers() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [servers, setServers] = useState<MCPServer[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // editing holds the server being edited, or "new" for the add form, or null
  // when the form is closed.
  const [editing, setEditing] = useState<MCPServer | "new" | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [confirmRemove, setConfirmRemove] = useState<string | null>(null);

  const load = useCallback(() => {
    if (!token) return;
    setLoading(true);
    api
      .listMCPServers(token)
      .then((r) => setServers(r.servers ?? []))
      .catch((e) => setError(explainApiError(e, t)))
      .finally(() => setLoading(false));
  }, [token, t]);

  useEffect(() => {
    load();
  }, [load]);

  const save = async (input: MCPServerInput, existingName?: string) => {
    if (!token) return;
    setError(null);
    // A create has no id yet — the daemon derives one — so the busy key is the
    // label until the row comes back with its name.
    setBusy(existingName ?? input.label);
    try {
      const saved = await api.saveMCPServer(token, input, existingName);
      setEditing(null);
      load();
      // A save that stored the row but could not connect is not an error — the
      // row is there and says why. Surfacing it here too is what stops someone
      // walking away from a server that will never work.
      if (saved.last_error) setError(t("mcp.savedButFailed", { error: saved.last_error }));
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(null);
    }
  };

  const refresh = async (name: string) => {
    if (!token) return;
    setError(null);
    setBusy(name);
    try {
      const updated = await api.refreshMCPServer(token, name);
      load();
      if (updated.last_error) setError(updated.last_error);
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(null);
    }
  };

  const remove = async (name: string) => {
    if (!token) return;
    setError(null);
    setConfirmRemove(null);
    setBusy(name);
    try {
      await api.deleteMCPServer(token, name);
      load();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(null);
    }
  };

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <Blocks size={ICON.xl} />
            {t("mcp.title")}
          </h1>
          <div className="sub">{t("mcp.subtitle")}</div>
        </div>
        <Button variant="primary" onClick={() => setEditing("new")} disabled={editing === "new"}>
          {t("mcp.add")}
        </Button>
      </div>

      {error && <ErrorNotice>{error}</ErrorNotice>}

      {editing && (
        <MCPServerForm
          server={editing === "new" ? null : editing}
          busy={busy !== null}
          onCancel={() => setEditing(null)}
          onSave={save}
        />
      )}

      {loading ? (
        <Loading />
      ) : servers.length === 0 ? (
        <EmptyState icon={Blocks} title={t("mcp.emptyTitle")}>
          {t("mcp.emptyBody")}
        </EmptyState>
      ) : (
        <div className="card">
          <div className="run-table-scroll">
            <table className="run-table">
              <thead>
                <tr>
                  <th>{t("common.name")}</th>
                  <th>{t("mcp.colEndpoint")}</th>
                  <th>{t("common.status")}</th>
                  <th>{t("mcp.colSteps")}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {servers.map((s) => (
                  <tr
                    key={s.name}
                    className={confirmRemove === s.name ? "row-confirming" : undefined}
                  >
                    <td className="run-name-cell">
                      {s.label}
                      {/* The id is shown under the name because it is what
                          appears in the palette and in flow JSON — an admin
                          looking for "MCP Test" among their steps needs to
                          know it is mcp-test there. */}
                      <div className="muted mcp-id">{s.name}</div>
                    </td>
                    {/* The URL is shown in full. It is the field most likely to
                        be wrong, and a truncated one cannot be checked. */}
                    <td className="muted mcp-url">{s.url}</td>
                    <td>
                      <MCPStatusChip server={s} />
                    </td>
                    <td>
                      <MCPTools server={s} />
                    </td>
                    <td className="runner-actions">
                      {confirmRemove === s.name ? (
                        <span className="inline-confirm">
                          {t("mcp.removeReally")}{" "}
                          <Button variant="danger" onClick={() => void remove(s.name)}>
                            {t("common.remove")}
                          </Button>
                          <Button variant="ghost" onClick={() => setConfirmRemove(null)}>
                            {t("common.cancel")}
                          </Button>
                        </span>
                      ) : (
                        <>
                          <Button
                            variant="ghost"
                            onClick={() => void refresh(s.name)}
                            disabled={busy === s.name}
                            title={t("mcp.refresh")}
                            aria-label={t("mcp.refresh")}
                          >
                            <RefreshCw size={ICON.sm} />
                          </Button>
                          <Button
                            variant="ghost"
                            onClick={() => setEditing(s)}
                            title={t("common.edit")}
                            aria-label={t("common.edit")}
                          >
                            <Pencil size={ICON.sm} />
                          </Button>
                          <Button
                            variant="ghost"
                            className="danger"
                            onClick={() => setConfirmRemove(s.name)}
                            title={t("common.remove")}
                            aria-label={t("common.remove")}
                          >
                            <Trash2 size={ICON.sm} />
                          </Button>
                        </>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      <Notice className="runner-warning">
        <AlertTriangle size={ICON.sm} className="icon-lede" />
        {t("mcp.securityNote")}
      </Notice>
    </div>
  );
}

// MCPServerForm is add and edit in one, because they are one operation.
//
// The name here is the DISPLAY name and is always editable. The id underneath
// it is derived from the name once, when the server is created, and then frozen
// — re-deriving it on an edit would silently re-key every step id the org's
// flows reference. The hint shows the id so the two are never confused.
function MCPServerForm({
  server,
  busy,
  onCancel,
  onSave,
}: {
  server: MCPServer | null;
  busy: boolean;
  onCancel: () => void;
  onSave: (input: MCPServerInput, existingName?: string) => void | Promise<void>;
}) {
  const { t } = useTranslation();
  const [label, setLabel] = useState(server?.label ?? "");
  const [url, setUrl] = useState(server?.url ?? "");
  const [authKind, setAuthKind] = useState<MCPServerInput["auth_kind"]>(server?.auth_kind ?? "none");
  const [authHeader, setAuthHeader] = useState(server?.auth_header ?? "");
  const [tokenValue, setTokenValue] = useState("");
  const [enabled, setEnabled] = useState(server?.enabled ?? true);

  // An existing credential is never sent back to the browser, so the field
  // starts empty and blank means "keep it". Saying so is the difference
  // between an admin leaving it alone and an admin hunting for the token.
  const keepsToken = !!server?.has_token && authKind !== "none";

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    void onSave(
      {
        label: label.trim(),
        url: url.trim(),
        auth_kind: authKind,
        auth_header: authKind === "header" ? authHeader.trim() : undefined,
        token: tokenValue ? tokenValue.trim() : undefined,
        enabled,
      },
      server?.name,
    );
  };

  return (
    <form className="card mcp-form" onSubmit={submit}>
      <h2>{server ? t("mcp.editHead", { name: server.label }) : t("mcp.addHead")}</h2>

      <div className="sf-field">
        <label htmlFor="mcp-name">{t("mcp.nameLabel")}</label>
        <input
          id="mcp-name"
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder="GitHub"
          maxLength={96}
          required
        />
        {/* Editable even on an existing server: the id was fixed at creation
            and is shown separately, so changing what people call it re-captions
            its steps without touching a single flow. */}
        <div className="desc">
          {server ? t("mcp.nameEditHint", { id: server.name }) : t("mcp.nameHint")}
        </div>
      </div>

      <div className="sf-field">
        <label htmlFor="mcp-url">{t("mcp.urlLabel")}</label>
        <input
          id="mcp-url"
          type="url"
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          placeholder="https://example.com/mcp"
          required
        />
        <div className="desc">{t("mcp.urlHint")}</div>
      </div>

      <div className="sf-field">
        <label htmlFor="mcp-auth">{t("mcp.authLabel")}</label>
        <select
          id="mcp-auth"
          value={authKind}
          onChange={(e) => setAuthKind(e.target.value as MCPServerInput["auth_kind"])}
        >
          <option value="none">{t("mcp.authNone")}</option>
          <option value="bearer">{t("mcp.authBearer")}</option>
          <option value="header">{t("mcp.authHeader")}</option>
        </select>
      </div>

      {authKind === "header" && (
        <div className="sf-field">
          <label htmlFor="mcp-header">{t("mcp.headerNameLabel")}</label>
          <input
            id="mcp-header"
            value={authHeader}
            onChange={(e) => setAuthHeader(e.target.value)}
            placeholder="X-Api-Key"
            required
          />
        </div>
      )}

      {authKind !== "none" && (
        <div className="sf-field">
          <label htmlFor="mcp-token">{t("mcp.tokenLabel")}</label>
          <input
            id="mcp-token"
            type="password"
            value={tokenValue}
            onChange={(e) => setTokenValue(e.target.value)}
            placeholder={keepsToken ? t("mcp.tokenKeepPlaceholder") : ""}
            autoComplete="off"
            required={!keepsToken}
          />
          <div className="desc">{t("mcp.tokenHint")}</div>
        </div>
      )}

      <div className="sf-field">
        <label className="checkbox-row">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          {t("mcp.enabledLabel")}
        </label>
        <div className="desc">{t("mcp.enabledHint")}</div>
      </div>

      <div className="runner-install-actions">
        <Button variant="primary" type="submit" disabled={busy}>
          {busy ? t("mcp.connecting") : t("mcp.saveAndConnect")}
        </Button>
        <Button type="button" onClick={onCancel}>
          {t("common.cancel")}
        </Button>
      </div>
    </form>
  );
}

// MCPStatusChip reuses the run-status vocabulary, so "connected" reads the way
// "succeeded" does everywhere else.
//
// Three states, not two. Disabled is not a failure and must not look like one:
// it is the reversible pause an admin chose.
function MCPStatusChip({ server }: { server: MCPServer }) {
  const { t } = useTranslation();
  if (!server.enabled) {
    return (
      <span className="status-chip">
        <span className="status-dot" />
        {t("mcp.disabled")}
      </span>
    );
  }
  const tone = server.connected ? "succeeded" : "failed";
  return (
    <span className={"status-chip " + tone} title={server.last_error || undefined}>
      <span className={"status-dot " + tone} />
      {server.connected
        ? t("mcp.connected")
        : server.last_error
          ? t("mcp.failing")
          : t("mcp.connecting")}
      {!server.connected && server.last_connected && (
        <> · {t("mcp.lastOk", { at: formatDateTime(server.last_connected) })}</>
      )}
    </span>
  );
}

// MCPTools shows what the org actually gained. A count alone leaves an admin
// guessing at what to search the palette for, so the first few ids are named.
function MCPTools({ server }: { server: MCPServer }) {
  const { t } = useTranslation();
  const ids = server.tool_ids ?? [];
  if (!ids.length) return <span className="muted">{server.tool_count || "—"}</span>;
  const shown = ids.slice(0, 3);
  return (
    <span className="muted mcp-tools" title={ids.join("\n")}>
      {shown.map((id) => id.split(":").slice(2).join(":")).join(", ")}
      {ids.length > shown.length && <> {t("mcp.andMore", { n: ids.length - shown.length })}</>}
    </span>
  );
}
