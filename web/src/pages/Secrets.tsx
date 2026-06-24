import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { api, APIError } from "../api";
import { useAuth } from "../auth";
import { ButtonLink } from "../components/Button";
import { CredentialsManager } from "../components/CredentialsManager";

// Secrets is the tenant's credential store: hand-entered values your
// flows reference as ${secret.NAME} (API keys, database URLs), plus an
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

// Secrets is the "Values" tab of the Admin → Secrets page (AdminSecrets):
// the tenant ${secret.NAME} vault. It renders bare (no page header) because
// AdminSecrets owns the title and tab bar.
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
    setSecrets(null);
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

  // Belt-and-suspenders: the daemon already hides managed/reserved entries,
  // but keep the client filter so oauth.*/conn.* never slip through if an
  // older daemon returns them.
  const userSecrets = (secrets ?? []).filter(
    (n) => !n.startsWith("oauth.") && !n.startsWith("conn."),
  );

  return (
    <>
      {secretsOff && <SetupIncompleteBanner supportContact={me?.support_contact} />}
      {error && <div className="card error">{error}</div>}

      {!secretsOff && (
        <>
          <p className="secret-scope-help">{t("connections.scopeHelp")}</p>
          <CredentialsManager
            secrets={userSecrets}
            loading={secrets === null}
            canWrite={canWrite}
            onChanged={refresh}
            // The editor's "Set up this credential" links route to
            // /admin/secrets?focus=NAME. Consumed once: scroll + highlight an
            // existing row or pre-fill the add-form, then strip the param so
            // a refresh doesn't re-fire the highlight.
            focus={searchParams.get("focus") ?? undefined}
            onFocusConsumed={() => {
              const next = new URLSearchParams(searchParams);
              next.delete("focus");
              setSearchParams(next, { replace: true });
            }}
          />
        </>
      )}
    </>
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
        <ButtonLink variant="primary" href={href}>
          {t("connections.setupIncompleteContact")}
        </ButtonLink>
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
