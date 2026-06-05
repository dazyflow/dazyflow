import { useEffect, useRef, useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { api, APIError, type SecretScope } from "../api";
import { useAuth } from "../auth";

// CredentialsManager lists hand-entered secrets (DB URLs, API tokens) by name
// — never value, the daemon has no read-back — with delete buttons and an add
// form. Reused by the Secrets page (tenant / workspace scope) and the flow
// editor's settings modal (flow scope). The `scope`/`flow` props route every
// mutation to the right scope; the parent owns fetching the scoped list and
// passes it in via `secrets`.
export function CredentialsManager({
  secrets,
  loading,
  canWrite,
  onChanged,
  scope,
  flow,
  focus,
  onFocusConsumed,
}: {
  secrets: string[];
  loading: boolean;
  canWrite: boolean;
  onChanged: () => void;
  // scope/flow default to tenant. flow is required for scope==="flow".
  scope?: SecretScope;
  flow?: string;
  // focus is a credential name the user was pointed at from a template
  // field's "Set up" link: scroll+highlight an existing row, else pre-fill
  // the add form. Consumed once via onFocusConsumed.
  focus?: string;
  onFocusConsumed?: () => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [highlighted, setHighlighted] = useState<string | null>(null);
  const valueInputRef = useRef<HTMLTextAreaElement | null>(null);
  const rowRefs = useRef<Map<string, HTMLLIElement | null>>(new Map());

  useEffect(() => {
    if (!focus || loading) return;
    if (secrets.includes(focus)) {
      const row = rowRefs.current.get(focus);
      row?.scrollIntoView({ behavior: "smooth", block: "center" });
      setHighlighted(focus);
      const handle = window.setTimeout(() => setHighlighted(null), 2000);
      onFocusConsumed?.();
      return () => window.clearTimeout(handle);
    }
    setName(focus);
    requestAnimationFrame(() => valueInputRef.current?.focus());
    onFocusConsumed?.();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focus, loading, secrets.join("\0")]);

  const add = async () => {
    if (!token) return;
    const trimmed = name.trim();
    if (!trimmed || !value) return;
    setBusy(true);
    setErr(null);
    try {
      await api.putSecret(token, trimmed, value, scope, flow);
      setName("");
      setValue("");
      onChanged();
    } catch (e) {
      setErr(e instanceof APIError ? e.message : (e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (n: string) => {
    if (!token) return;
    if (!window.confirm(t("connections.deleteConfirm", { name: n }))) return;
    try {
      await api.deleteSecret(token, n, scope, flow);
      onChanged();
    } catch (e) {
      setErr(e instanceof APIError ? e.message : (e as Error).message);
    }
  };

  return (
    <div className="credentials">
      {err && <div className="card error">{err}</div>}
      {loading ? (
        <div className="card">{t("common.loading")}</div>
      ) : secrets.length === 0 ? (
        <p className="credentials-empty">{t("connections.noSecrets")}</p>
      ) : (
        <ul className="credentials-list">
          {secrets.map((n) => (
            <li
              key={n}
              ref={(el) => {
                rowRefs.current.set(n, el);
              }}
              className={
                "credentials-item" +
                (highlighted === n ? " credentials-item-highlight" : "")
              }
            >
              <code>{n}</code>
              <span className="credentials-set">{t("connections.valueSet")}</span>
              {canWrite && (
                <button
                  type="button"
                  className="icon-button danger"
                  aria-label={t("connections.deleteSecret", { name: n })}
                  title={t("connections.deleteSecret", { name: n })}
                  onClick={() => remove(n)}
                >
                  <Trash2 size={15} />
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {canWrite && (
        <form
          className="credentials-add"
          onSubmit={(e) => {
            e.preventDefault();
            void add();
          }}
        >
          <input
            type="text"
            placeholder={t("connections.namePlaceholder")}
            value={name}
            onChange={(e) => setName(e.target.value)}
            aria-label={t("connections.nameLabel")}
          />
          <textarea
            ref={valueInputRef}
            rows={4}
            placeholder={t("connections.valuePlaceholder")}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            aria-label={t("connections.valueLabel")}
            autoComplete="off"
            spellCheck={false}
          />
          <button
            type="submit"
            className="primary"
            disabled={busy || !name.trim() || !value}
          >
            <Plus size={15} /> {busy ? t("connections.saving") : t("connections.addSecret")}
          </button>
        </form>
      )}
    </div>
  );
}
