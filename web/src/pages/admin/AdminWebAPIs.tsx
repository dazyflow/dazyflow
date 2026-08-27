// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { Globe, Pencil, Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "../../components/ui/Button";
import { useAuth } from "../../auth";
import { api } from "../../api";
import type {
  StepSourceUsage,
  WebAPI,
  WebAPIArg,
  WebAPIInput,
  WebAPILogoMode,
  WebAPIOperation,
} from "../../types";
import { fileToLogo, LOGO_ACCEPT } from "../../lib/logoUpload";
import { StepSourceRemoveWarning } from "../../components/admin/StepSourceRemoveWarning";
import { explainApiError } from "../../lib/explainApiError";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { EmptyState } from "../../components/ui/EmptyState";
import { Loading } from "../../components/ui/Loading";
import { Notice } from "../../components/ui/Notice";
import { ICON } from "../../icons";

// AdminWebAPIs is where an org describes its OWN service and gets steps out of
// it.
//
// The difference from Admin → MCP servers is worth stating, because the two
// pages look alike: there, saving CONNECTS, and the thing that can go wrong is
// invisible until something tries. Here there is nothing to dial — a described
// API is a document — so a save that returns is a save that put the steps in the
// palette, and the whole of the feedback is validation. That is why this page
// spends its effort on the operation editor and shows no "connected" chip: a
// green light claiming health would be a lie about a call nobody made.
export function AdminWebAPIs() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [apis, setApis] = useState<WebAPI[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState<WebAPI | "new" | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [confirmRemove, setConfirmRemove] = useState<string | null>(null);
  // usage is what the catalog being confirmed is actually used by, keyed by
  // name. undefined means "still asking" — the confirm renders without a count
  // rather than waiting, so the button is never dead while a scan of the org's
  // graphs runs.
  const [usage, setUsage] = useState<Record<string, StepSourceUsage>>({});

  const load = useCallback(() => {
    if (!token) return;
    setLoading(true);
    api
      .listWebAPIs(token)
      .then((r) => setApis(r.web_apis ?? []))
      .catch((e) => setError(explainApiError(e, t)))
      .finally(() => setLoading(false));
  }, [token, t]);

  useEffect(() => {
    load();
  }, [load]);

  const save = async (input: WebAPIInput, existingName?: string) => {
    if (!token) return;
    setError(null);
    // A create has no id yet — the daemon derives one — so the busy key is the
    // label until the row comes back with its name.
    setBusy(existingName ?? input.label);
    try {
      const saved = await api.saveWebAPI(token, input, existingName);
      setEditing(null);
      load();
      // Normally empty. It is set only for a stored catalog the current release
      // refuses, which an admin can otherwise only experience as steps that
      // quietly went missing.
      if (saved.last_error)
        setError(t("webapi.savedButBroken", { error: saved.last_error }));
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(null);
    }
  };

  // askToRemove opens the confirmation and, in parallel, finds out what the
  // catalog is used by. The warning fills in when the answer arrives.
  const askToRemove = (name: string) => {
    setConfirmRemove(name);
    if (!token || usage[name]) return;
    api
      .webAPIUsage(token, name)
      .then((u) => setUsage((prev) => ({ ...prev, [name]: u })))
      // A failed lookup must not block the delete or claim nothing is using the
      // catalog. The confirm falls back to the unconditional warning.
      .catch(() => {});
  };

  const remove = async (name: string) => {
    if (!token) return;
    setError(null);
    setConfirmRemove(null);
    setBusy(name);
    try {
      await api.deleteWebAPI(token, name);
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
            <Globe size={ICON.xl} />
            {t("webapi.title")}
          </h1>
          <div className="sub">{t("webapi.subtitle")}</div>
        </div>
        <Button
          variant="primary"
          onClick={() => setEditing("new")}
          disabled={editing === "new"}
        >
          {t("webapi.add")}
        </Button>
      </div>

      {error && <ErrorNotice>{error}</ErrorNotice>}

      {editing && (
        <WebAPIForm
          webapi={editing === "new" ? null : editing}
          busy={busy !== null}
          onCancel={() => setEditing(null)}
          onSave={save}
        />
      )}

      {loading ? (
        <Loading />
      ) : apis.length === 0 ? (
        <EmptyState icon={Globe} title={t("webapi.emptyTitle")}>
          {t("webapi.emptyBody")}
        </EmptyState>
      ) : (
        <div className="card">
          <div className="run-table-scroll">
            <table className="run-table">
              <thead>
                <tr>
                  <th>{t("common.name")}</th>
                  <th>{t("webapi.colAddress")}</th>
                  <th>{t("common.status")}</th>
                  <th>{t("webapi.colSteps")}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {apis.map((w) => (
                  <tr
                    key={w.name}
                    className={
                      confirmRemove === w.name ? "row-confirming" : undefined
                    }
                  >
                    <td className="run-name-cell">
                      <span className="step-source-name">
                        {/* The mark the palette will use, guessed from the
                            service's favicon. Shown here because a guess that
                            landed on the wrong logo is worth seeing where the
                            address that produced it can be corrected — and
                            because a save is what re-runs the guess. */}
                        {w.logo ? (
                          <img
                            className="step-source-logo"
                            src={w.logo}
                            alt=""
                            draggable={false}
                          />
                        ) : null}
                        {w.label}
                      </span>
                      {/* The id is shown under the name because it is what
                          appears in the palette and in flow JSON — an admin
                          looking for "Order service" among their steps needs to
                          know it is api:order-service:… there. */}
                      <div className="muted mcp-id">{w.name}</div>
                    </td>
                    {/* Shown in full: it is the field most likely to be wrong,
                        and a truncated address cannot be checked. */}
                    <td className="muted mcp-url">{w.base_url}</td>
                    <td>
                      <WebAPIStatusChip webapi={w} />
                    </td>
                    <td>
                      <WebAPISteps webapi={w} />
                    </td>
                    <td className="runner-actions">
                      {confirmRemove === w.name ? (
                        <span className="inline-confirm">
                          <StepSourceRemoveWarning usage={usage[w.name]} ns="webapi" />{" "}
                          <Button
                            variant="danger"
                            onClick={() => void remove(w.name)}
                          >
                            {t("common.remove")}
                          </Button>
                          <Button
                            variant="ghost"
                            onClick={() => setConfirmRemove(null)}
                          >
                            {t("common.cancel")}
                          </Button>
                        </span>
                      ) : (
                        <>
                          <Button
                            variant="ghost"
                            onClick={() => setEditing(w)}
                            title={t("common.edit")}
                            aria-label={t("common.edit")}
                          >
                            <Pencil size={ICON.sm} />
                          </Button>
                          <Button
                            variant="ghost"
                            className="danger"
                            onClick={() => askToRemove(w.name)}
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

      <Notice className="runner-warning">{t("webapi.connectionNote")}</Notice>
    </div>
  );
}

// blankOperation is what "add an operation" starts from. GET with no body is the
// commonest shape and the one that needs the least filling in.
const blankOperation = (): WebAPIOperation => ({
  id: "",
  method: "GET",
  path: "/",
  body_mode: "none",
  args: [],
});

const blankArg = (): WebAPIArg => ({ name: "", in: "query", type: "string" });

// WebAPIForm is add and edit in one, because they are one operation.
//
// The name here is the DISPLAY name and is always editable. The id underneath it
// is derived from the name once, at creation, and then frozen — re-deriving it
// on an edit would silently re-key every step id the org's flows reference.
function WebAPIForm({
  webapi,
  busy,
  onCancel,
  onSave,
}: {
  webapi: WebAPI | null;
  busy: boolean;
  onCancel: () => void;
  onSave: (input: WebAPIInput, existingName?: string) => void | Promise<void>;
}) {
  const { t } = useTranslation();
  const [label, setLabel] = useState(webapi?.label ?? "");
  const [description, setDescription] = useState(webapi?.description ?? "");
  const [baseURL, setBaseURL] = useState(webapi?.base_url ?? "");
  const [authKind, setAuthKind] = useState<WebAPIInput["auth_kind"]>(
    webapi?.auth_kind ?? "bearer",
  );
  const [authHeader, setAuthHeader] = useState(webapi?.auth_header ?? "");
  const [enabled, setEnabled] = useState(webapi?.enabled ?? true);
  const [logoMode, setLogoMode] = useState<WebAPILogoMode>(
    webapi?.logo_mode ?? "auto",
  );
  const [logo, setLogo] = useState(webapi?.logo ?? "");
  const [logoError, setLogoError] = useState<string | null>(null);
  const [operations, setOperations] = useState<WebAPIOperation[]>(
    webapi?.operations?.length ? webapi.operations : [blankOperation()],
  );

  const patchOperation = (i: number, patch: Partial<WebAPIOperation>) =>
    setOperations((ops) =>
      ops.map((op, n) => (n === i ? { ...op, ...patch } : op)),
    );

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    void onSave(
      {
        label: label.trim(),
        description: description.trim(),
        base_url: baseURL.trim(),
        auth_kind: authKind,
        auth_header: authKind === "header" ? authHeader.trim() : undefined,
        enabled,
        logo_mode: logoMode,
        // Only sent for the mode that reads it. The stored image is resent
        // unchanged when the admin did not pick a new file, which is what makes
        // "edit the address" keep the mark.
        logo: logoMode === "custom" ? logo : undefined,
        operations: operations.map((op) => ({
          ...op,
          id: op.id.trim(),
          path: op.path.trim(),
          args: (op.args ?? []).map((a) => ({ ...a, name: a.name.trim() })),
        })),
      },
      webapi?.name,
    );
  };

  return (
    <form className="card mcp-form" onSubmit={submit}>
      <h2>
        {webapi
          ? t("webapi.editHead", { name: webapi.label })
          : t("webapi.addHead")}
      </h2>

      <div className="sf-field">
        <label htmlFor="wa-name">{t("common.name")}</label>
        <input
          id="wa-name"
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder="Order service"
          maxLength={96}
          required
        />
        <div className="desc">
          {webapi
            ? t("webapi.nameEditHint", { id: webapi.name })
            : t("webapi.nameHint")}
        </div>
      </div>

      {/* The blurb the Apps page shows under this app's name. It is the one
          piece of prose about an org's own service that nobody else can write:
          every built-in app's description is curated in the product, and there
          is nowhere to curate an org's. */}
      <div className="sf-field">
        <label htmlFor="wa-description">{t("webapi.descriptionLabel")}</label>
        <textarea
          id="wa-description"
          rows={3}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder={t("webapi.descriptionPlaceholder")}
          maxLength={600}
        />
        <div className="desc">{t("webapi.descriptionHint")}</div>
      </div>

      <div className="sf-field">
        <label htmlFor="wa-url">{t("webapi.urlLabel")}</label>
        <input
          id="wa-url"
          type="url"
          value={baseURL}
          onChange={(e) => setBaseURL(e.target.value)}
          placeholder="https://api.example.com/v1"
          required
        />
        <div className="desc">{t("webapi.urlHint")}</div>
      </div>

      <div className="sf-field">
        <label htmlFor="wa-auth">{t("webapi.authLabel")}</label>
        <select
          id="wa-auth"
          value={authKind}
          onChange={(e) =>
            setAuthKind(e.target.value as WebAPIInput["auth_kind"])
          }
        >
          <option value="none">{t("webapi.authNone")}</option>
          <option value="bearer">{t("webapi.authBearer")}</option>
          <option value="header">{t("webapi.authHeader")}</option>
        </select>
        {/* The credential itself is NOT on this page, and saying so here is the
            difference between an admin filling in the connection and an admin
            hunting for a token field that does not exist. */}
        <div className="desc">{t("webapi.authHint")}</div>
      </div>

      {authKind === "header" && (
        <div className="sf-field">
          <label htmlFor="wa-header">{t("webapi.headerNameLabel")}</label>
          <input
            id="wa-header"
            value={authHeader}
            onChange={(e) => setAuthHeader(e.target.value)}
            placeholder="X-Api-Key"
            required
          />
        </div>
      )}

      {/* The mark every step of this catalog will wear. Three sources rather
          than one upload field, because a guess that found nothing has to be
          retried on the next save while a glyph the admin CHOSE must not be —
          the two are the same empty image and only this choice tells them
          apart. */}
      <div className="sf-field">
        <label htmlFor="wa-logo-mode">{t("webapi.iconLabel")}</label>
        <div className="step-source-icon-row">
          <span className="step-source-icon-preview">
            {logoMode !== "none" && logo ? (
              <img src={logo} alt="" draggable={false} />
            ) : (
              <Globe size={ICON.lg} />
            )}
          </span>
          <select
            id="wa-logo-mode"
            value={logoMode}
            onChange={(e) => {
              setLogoError(null);
              setLogoMode(e.target.value as WebAPILogoMode);
            }}
          >
            <option value="auto">{t("webapi.iconAuto")}</option>
            <option value="custom">{t("webapi.iconCustom")}</option>
            <option value="none">{t("webapi.iconNone")}</option>
          </select>
        </div>
        {logoMode === "custom" && (
          <input
            type="file"
            aria-label={t("webapi.iconFileLabel")}
            accept={LOGO_ACCEPT}
            onChange={(e) => {
              const file = e.target.files?.[0];
              if (!file) return;
              setLogoError(null);
              void fileToLogo(file).then(
                (uri) => setLogo(uri),
                // The codes fileToLogo rejects with each need their own
                // suggestion, so they are keys rather than sentences.
                (err: Error) => setLogoError(t(`webapi.icon_${err.message}`)),
              );
            }}
          />
        )}
        {logoError && <div className="field-error">{logoError}</div>}
        <div className="desc">{t("webapi.iconHint")}</div>
      </div>

      <h3>{t("webapi.operationsHead")}</h3>
      <div className="desc">{t("webapi.operationsHint")}</div>

      {operations.map((op, i) => (
        <OperationEditor
          key={i}
          op={op}
          index={i}
          removable={operations.length > 1}
          onPatch={(patch) => patchOperation(i, patch)}
          onRemove={() => setOperations((ops) => ops.filter((_, n) => n !== i))}
        />
      ))}

      <Button
        type="button"
        onClick={() => setOperations((ops) => [...ops, blankOperation()])}
      >
        <Plus size={ICON.sm} /> {t("webapi.addOperation")}
      </Button>

      <div className="sf-field">
        <label className="checkbox-row">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          {t("webapi.enabledLabel")}
        </label>
        <div className="desc">{t("webapi.enabledHint")}</div>
      </div>

      <div className="runner-install-actions">
        <Button variant="primary" type="submit" disabled={busy}>
          {busy ? t("webapi.saving") : t("webapi.save")}
        </Button>
        <Button type="button" onClick={onCancel}>
          {t("common.cancel")}
        </Button>
      </div>
    </form>
  );
}

// OperationEditor is one call, described.
//
// The argument list is the part that earns the space: a step is only better than
// a generic web request because its arguments are named and typed, so this is
// where the value of the whole feature is entered.
function OperationEditor({
  op,
  index,
  removable,
  onPatch,
  onRemove,
}: {
  op: WebAPIOperation;
  index: number;
  removable: boolean;
  onPatch: (patch: Partial<WebAPIOperation>) => void;
  onRemove: () => void;
}) {
  const { t } = useTranslation();
  const args = op.args ?? [];
  const patchArg = (i: number, patch: Partial<WebAPIArg>) =>
    onPatch({ args: args.map((a, n) => (n === i ? { ...a, ...patch } : a)) });

  // A body argument is only assemblable when the operation sends a JSON body,
  // and the daemon refuses the save otherwise. Offering the choice only when it
  // is legal is better than explaining the refusal afterwards.
  const bodyAllowed = op.body_mode === "json";

  return (
    <fieldset className="card webapi-op">
      <legend>
        {t("webapi.operationN", { n: index + 1 })}
        {removable && (
          <Button
            variant="ghost"
            className="danger"
            type="button"
            onClick={onRemove}
            title={t("common.remove")}
            aria-label={t("webapi.removeOperation")}
          >
            <Trash2 size={ICON.sm} />
          </Button>
        )}
      </legend>

      {/* The name comes first because it is the field that decides how this
          operation reads in the palette. Without one the step is captioned by
          its id — "order-service — get_order" — which is an identifier, not a
          name. The id stays below it, and stays frozen. */}
      <div className="sf-field">
        <label htmlFor={`wa-op-${index}-title`}>{t("webapi.opTitleLabel")}</label>
        <input
          id={`wa-op-${index}-title`}
          value={op.title ?? ""}
          onChange={(e) => onPatch({ title: e.target.value })}
          placeholder="Fetch an order"
          maxLength={96}
        />
        <div className="desc">{t("webapi.opTitleHint")}</div>
      </div>

      <div className="webapi-op-row">
        <div className="sf-field">
          <label htmlFor={`wa-op-${index}-id`}>{t("webapi.opIdLabel")}</label>
          <input
            id={`wa-op-${index}-id`}
            value={op.id}
            onChange={(e) => onPatch({ id: e.target.value })}
            placeholder="get_order"
            required
          />
          <div className="desc">{t("webapi.opIdHint")}</div>
        </div>

        <div className="sf-field">
          <label htmlFor={`wa-op-${index}-method`}>
            {t("webapi.opMethodLabel")}
          </label>
          <select
            id={`wa-op-${index}-method`}
            value={op.method}
            onChange={(e) =>
              onPatch({ method: e.target.value as WebAPIOperation["method"] })
            }
          >
            {(["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"] as const).map(
              (m) => (
                <option key={m} value={m}>
                  {m}
                </option>
              ),
            )}
          </select>
        </div>

        <div className="sf-field">
          <label htmlFor={`wa-op-${index}-path`}>
            {t("webapi.opPathLabel")}
          </label>
          <input
            id={`wa-op-${index}-path`}
            value={op.path}
            onChange={(e) => onPatch({ path: e.target.value })}
            placeholder="/orders/{order_id}"
            required
          />
          <div className="desc">{t("webapi.opPathHint")}</div>
        </div>
      </div>

      <div className="sf-field">
        <label htmlFor={`wa-op-${index}-summary`}>
          {t("webapi.opSummaryLabel")}
        </label>
        <input
          id={`wa-op-${index}-summary`}
          value={op.summary ?? ""}
          onChange={(e) => onPatch({ summary: e.target.value })}
          placeholder={t("webapi.opSummaryPlaceholder")}
        />
        {/* This is what the palette and the flow generator read. An operation
            with no summary is a step nobody can find by searching for what it
            does. */}
        <div className="desc">{t("webapi.opSummaryHint")}</div>
      </div>

      <div className="sf-field">
        <label htmlFor={`wa-op-${index}-body`}>{t("webapi.opBodyLabel")}</label>
        <select
          id={`wa-op-${index}-body`}
          value={op.body_mode ?? "none"}
          onChange={(e) => {
            const mode = e.target.value as WebAPIOperation["body_mode"];
            // Switching away from a JSON body would leave body arguments the
            // daemon must refuse, so they move to the query string rather than
            // being silently dropped: the admin typed them, and a save that
            // erases input is worse than one that relocates it visibly.
            const rehomed =
              mode === "json"
                ? args
                : args.map((a) =>
                    a.in === "body" ? { ...a, in: "query" as const } : a,
                  );
            onPatch({ body_mode: mode, args: rehomed });
          }}
        >
          <option value="none">{t("webapi.bodyNone")}</option>
          <option value="json">{t("webapi.bodyJSON")}</option>
          <option value="raw">{t("webapi.bodyRaw")}</option>
        </select>
        <div className="desc">{t("webapi.opBodyHint")}</div>
      </div>

      <div className="webapi-args">
        <div className="desc">{t("webapi.argsHead")}</div>
        {args.map((a, i) => (
          <div className="webapi-arg-row" key={i}>
            <input
              value={a.name}
              onChange={(e) => patchArg(i, { name: e.target.value })}
              placeholder={t("webapi.argNamePlaceholder")}
              aria-label={t("webapi.argNameLabel")}
              required
            />
            <select
              value={a.in}
              onChange={(e) =>
                patchArg(i, { in: e.target.value as WebAPIArg["in"] })
              }
              aria-label={t("webapi.argInLabel")}
            >
              <option value="path">{t("webapi.inPath")}</option>
              <option value="query">{t("webapi.inQuery")}</option>
              <option value="header">{t("webapi.inHeader")}</option>
              {bodyAllowed && (
                <option value="body">{t("webapi.inBody")}</option>
              )}
            </select>
            <select
              value={a.type ?? "string"}
              onChange={(e) => patchArg(i, { type: e.target.value })}
              aria-label={t("webapi.argTypeLabel")}
            >
              <option value="string">{t("webapi.typeString")}</option>
              <option value="integer">{t("webapi.typeInteger")}</option>
              <option value="number">{t("webapi.typeNumber")}</option>
              <option value="boolean">{t("webapi.typeBoolean")}</option>
            </select>
            <label className="checkbox-row">
              <input
                type="checkbox"
                checked={!!a.required}
                onChange={(e) => patchArg(i, { required: e.target.checked })}
              />
              {t("webapi.argRequired")}
            </label>
            <Button
              variant="ghost"
              className="danger"
              type="button"
              onClick={() => onPatch({ args: args.filter((_, n) => n !== i) })}
              title={t("common.remove")}
              aria-label={t("webapi.removeArgument")}
            >
              <Trash2 size={ICON.sm} />
            </Button>
          </div>
        ))}
        <Button
          type="button"
          onClick={() => onPatch({ args: [...args, blankArg()] })}
        >
          <Plus size={ICON.sm} /> {t("webapi.addArgument")}
        </Button>
      </div>
    </fieldset>
  );
}

// WebAPIStatusChip has three states and none of them claims the service is
// reachable.
//
// "In your palette" is the honest strong state: the daemon holds the catalog and
// the steps resolve. Whether the service answers is knowable only from a run,
// and a chip that implied otherwise would be the one lie this page could tell.
function WebAPIStatusChip({ webapi }: { webapi: WebAPI }) {
  const { t } = useTranslation();
  if (!webapi.enabled) {
    return (
      <span className="status-chip">
        <span className="status-dot" />
        {t("webapi.disabled")}
      </span>
    );
  }
  // The pill stays neutral and the DOT carries the tone, which is how every
  // status chip in the app works: only `.status-dot.<tone>` has rules. Six call
  // sites used to pass the tone to the pill as well, where it matched nothing;
  // CI's class guard caught this one (the only one written as a literal) and the
  // rest were cleaned out with it.
  if (webapi.last_error) {
    return (
      <span className="status-chip" title={webapi.last_error}>
        <span className="status-dot failed" />
        {t("webapi.broken")}
      </span>
    );
  }
  return (
    <span className="status-chip">
      <span className={webapi.registered ? "status-dot succeeded" : "status-dot"} />
      {webapi.registered ? t("webapi.inPalette") : t("webapi.pending")}
    </span>
  );
}

// WebAPISteps shows what the org actually gained. A count alone leaves an admin
// guessing at what to search the palette for, so the first few ids are named.
function WebAPISteps({ webapi }: { webapi: WebAPI }) {
  const { t } = useTranslation();
  const ids = webapi.step_ids ?? [];
  if (!ids.length)
    return <span className="muted">{webapi.operations?.length || "—"}</span>;
  const shown = ids.slice(0, 3);
  return (
    <span className="muted mcp-tools" title={ids.join("\n")}>
      {shown.map((id) => id.split(":").slice(2).join(":")).join(", ")}
      {ids.length > shown.length && (
        <> {t("webapi.andMore", { n: ids.length - shown.length })}</>
      )}
    </span>
  );
}
