import { useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { Plus, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { api, APIError } from "../api";
import { useAuth } from "../auth";

// Secrets is the tenant's credential store: hand-entered values your
// flows reference as ${tenant:NAME} (API keys, database URLs), plus an
// optional bring-your-own secret manager (Vault/OpenBao). Connecting
// apps (OAuth + per-app keys) lives on the Apps pages now — this page
// is purely the raw secret values that aren't tied to a known app.
//
// Each section hides itself when the daemon reports the feature isn't
// configured (501) or the caller can't use it (401/403), so a minimal
// install or a low-privilege user doesn't see dead controls.

// featureUnavailable: statuses that mean "this feature isn't usable for
// this caller" — not configured (501) or not permitted (401/403). All
// map to "hide the section" rather than an error banner.
function featureUnavailable(status: number): boolean {
  return status === 501 || status === 401 || status === 403;
}

export function Secrets() {
  const { t } = useTranslation();
  const { token, me, hasPerm } = useAuth();
  const [searchParams, setSearchParams] = useSearchParams();

  // canWrite gates every mutating control on the page. The read
  // endpoint only needs secret:read, so a read-only role can land here
  // and see the stored names — but must not be shown Add / Delete
  // affordances that would just 403 on click.
  const canWrite = hasPerm("secret:write");

  const [secrets, setSecrets] = useState<string[] | null>(null);
  const [secretsOff, setSecretsOff] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = () => {
    if (!token) return;
    api
      .listSecrets(token)
      .then((r) => {
        setSecrets(r.secrets);
        setSecretsOff(false);
      })
      .catch((e) => {
        if (e instanceof APIError && featureUnavailable(e.status))
          setSecretsOff(true);
        else setError(e instanceof APIError ? e.message : (e as Error).message);
      });
  };

  useEffect(refresh, [token]);

  // Hide managed entries: oauth.* are connected-app tokens (Apps OAuth
  // flow) and conn/* are multi-field service-connection fields (the Apps
  // connection card) — neither is an ad-hoc value the user types here.
  const userSecrets = (secrets ?? []).filter(
    (n) => !n.startsWith("oauth.") && !n.startsWith("conn."),
  );

  return (
    <div className="page connections-page">
      <h1>{t("connections.title")}</h1>
      <p className="page-sub">{t("connections.intro")}</p>

      {secretsOff && <SetupIncompleteBanner supportContact={me?.support_contact} />}
      {error && <div className="card error">{error}</div>}

      {!secretsOff && (
        <CredentialsManager
          secrets={userSecrets}
          loading={secrets === null}
          canWrite={canWrite}
          onChanged={refresh}
          // The editor's "Set up this credential" links route to
          // /secrets?focus=NAME so a user landing here from a
          // template field knows which value they need to add.
          // Consumed once on mount: the credentials manager scrolls
          // + highlights an existing row or pre-fills the add-form;
          // we strip the param afterwards so a refresh doesn't
          // re-fire the highlight.
          focus={searchParams.get("focus") ?? undefined}
          onFocusConsumed={() => {
            const next = new URLSearchParams(searchParams);
            next.delete("focus");
            setSearchParams(next, { replace: true });
          }}
        />
      )}
    </div>
  );
}

// SetupIncompleteBanner replaces the bare "feature off" card when BOTH
// OAuth and the encrypted secret store come back unavailable. The
// page would otherwise be empty save the title — leaving a paying
// end-user with no path forward. The banner names the situation,
// pins the responsibility on the operator (not the end user), and
// gives them somewhere to click when a support contact is set.
function SetupIncompleteBanner({
  supportContact,
}: {
  supportContact?: string;
}) {
  const { t } = useTranslation();
  const href = supportContactHref(supportContact);
  return (
    <div className="card connections-setup-incomplete" role="status">
      <h2 className="connections-setup-incomplete-title">
        {t("connections.setupIncompleteTitle")}
      </h2>
      <p>{t("connections.setupIncompleteBody")}</p>
      {href ? (
        <a className="primary" href={href}>
          {t("connections.setupIncompleteContact")}
        </a>
      ) : (
        <p className="connections-setup-incomplete-fallback">
          {t("connections.setupIncompleteContactGeneric")}
        </p>
      )}
    </div>
  );
}

// supportContactHref turns an operator-set contact string into the
// right `href`. We accept three shapes so the operator doesn't have
// to think about escaping:
//   - "support@acme.com"          → mailto:support@acme.com
//   - "https://acme.com/help"     → as-is
//   - "http://acme.com/help"      → as-is
// Anything else returns undefined, which falls back to the generic
// "ask your admin" copy (no clickable link).
function supportContactHref(raw?: string): string | undefined {
  const trimmed = raw?.trim();
  if (!trimmed) return undefined;
  if (trimmed.startsWith("http://") || trimmed.startsWith("https://")) {
    return trimmed;
  }
  if (trimmed.startsWith("mailto:")) return trimmed;
  // Email heuristic: `local@domain` with no whitespace. Good enough
  // for the operator-input use case; this isn't an RFC validator.
  if (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed)) {
    return `mailto:${trimmed}`;
  }
  return undefined;
}

// CredentialsManager lists the hand-entered secrets (DB URLs, API
// tokens) by name — never value, the daemon has no read-back — with
// delete buttons, plus an add form. Used for the ${tenant://NAME} /
// ${env:NAME} references a template needs that aren't OAuth.
function CredentialsManager({
  secrets,
  loading,
  canWrite,
  onChanged,
  focus,
  onFocusConsumed,
}: {
  secrets: string[];
  loading: boolean;
  canWrite: boolean;
  onChanged: () => void;
  // focus is a credential name the user was pointed at from
  // somewhere else (a template field's "Set up" link). When the
  // secret already exists, the matching row scrolls into view and
  // highlights briefly; otherwise the add-form pre-fills with the
  // name + the value input takes focus so the user can finish
  // setting it up in one step. onFocusConsumed strips the query
  // param so a refresh doesn't re-fire the highlight.
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
  const valueInputRef = useRef<HTMLInputElement | null>(null);
  const rowRefs = useRef<Map<string, HTMLLIElement | null>>(new Map());

  // Apply the inbound ?focus= once. We wait until the secrets list
  // has actually loaded (so we know whether the credential exists
  // or not) — calling onFocusConsumed only after we've acted means
  // a slow secrets fetch doesn't drop the focus on the floor.
  useEffect(() => {
    if (!focus || loading) return;
    if (secrets.includes(focus)) {
      // Existing row — scroll to it and pulse the highlight class.
      const row = rowRefs.current.get(focus);
      row?.scrollIntoView({ behavior: "smooth", block: "center" });
      setHighlighted(focus);
      const handle = window.setTimeout(() => setHighlighted(null), 2000);
      onFocusConsumed?.();
      return () => window.clearTimeout(handle);
    }
    // New credential — pre-fill the name, focus the value input so
    // the user can type the secret without an extra click.
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
      await api.putSecret(token, trimmed, value);
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
      await api.deleteSecret(token, n);
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
        <p className="credentials-empty">{t("connections.noCredentials")}</p>
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
                  aria-label={t("connections.deleteCredential", { name: n })}
                  title={t("connections.deleteCredential", { name: n })}
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
          <input
            ref={valueInputRef}
            type="password"
            placeholder={t("connections.valuePlaceholder")}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            aria-label={t("connections.valueLabel")}
            autoComplete="off"
          />
          <button
            type="submit"
            className="primary"
            disabled={busy || !name.trim() || !value}
          >
            <Plus size={15} /> {busy ? t("connections.saving") : t("connections.addCredential")}
          </button>
        </form>
      )}
    </div>
  );
}
