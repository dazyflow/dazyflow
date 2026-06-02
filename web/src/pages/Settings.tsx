import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Moon, ShieldCheck, Sun } from "lucide-react";
import { applyTheme, getTheme, type ThemeMode } from "../theme";
import { useAuth } from "../auth";
import { api, APIError, type TOTPSetup, type TOTPStatus } from "../api";

// Settings is the per-user, per-browser preferences page — reached
// from the account menu in the sidebar. Holds appearance + language,
// both stored client-side only (localStorage); there are no
// server-side user prefs yet, so switching here is instant and local.
export function Settings() {
  const { t, i18n } = useTranslation();

  const languages = [
    { code: "en", label: t("appSettings.langEnglish") },
    { code: "sv", label: t("appSettings.langSwedish") },
  ];
  // i18n.resolvedLanguage collapses regional codes (sv-SE → sv) to the
  // bundle that's actually active, so the <select> reflects reality.
  const currentLang = i18n.resolvedLanguage ?? i18n.language ?? "en";

  // Theme is applied imperatively (data-theme on <html>); keep a local
  // mirror just to drive the selected-state on the two cards.
  const [theme, setTheme] = useState<ThemeMode>(getTheme());
  const pickTheme = (mode: ThemeMode) => {
    applyTheme(mode);
    setTheme(mode);
  };

  return (
    <div className="page settings-page">
      <h1>{t("appSettings.title")}</h1>
      <p className="page-sub">{t("appSettings.subtitle")}</p>

      <div className="card settings-card">
        <div className="sf-field">
          <div className="label-row">
            <label>{t("appSettings.themeLabel")}</label>
          </div>
          <div className="theme-choice">
            <button
              type="button"
              className={"theme-option" + (theme === "dark" ? " active" : "")}
              aria-pressed={theme === "dark"}
              onClick={() => pickTheme("dark")}
            >
              <span className="theme-swatch theme-swatch-dark" aria-hidden="true">
                <Moon size={16} />
              </span>
              <span className="theme-option-label">{t("appSettings.themeDark")}</span>
            </button>
            <button
              type="button"
              className={"theme-option" + (theme === "light" ? " active" : "")}
              aria-pressed={theme === "light"}
              onClick={() => pickTheme("light")}
            >
              <span className="theme-swatch theme-swatch-light" aria-hidden="true">
                <Sun size={16} />
              </span>
              <span className="theme-option-label">{t("appSettings.themeLight")}</span>
            </button>
          </div>
          <div className="desc">{t("appSettings.themeDesc")}</div>
        </div>

        <div className="sf-field">
          <div className="label-row">
            <label htmlFor="lang-select">{t("appSettings.languageLabel")}</label>
          </div>
          <select
            id="lang-select"
            value={currentLang}
            onChange={(e) => {
              // changeLanguage swaps the active catalogue AND, via the
              // languagedetector's localStorage cache, persists the
              // choice so it survives reloads.
              void i18n.changeLanguage(e.target.value);
            }}
          >
            {languages.map((l) => (
              <option key={l.code} value={l.code}>
                {l.label}
              </option>
            ))}
          </select>
          <div className="desc">{t("appSettings.languageDesc")}</div>
        </div>
      </div>

      <TwoFactorCard />
    </div>
  );
}

// TwoFactorCard manages the signed-in user's TOTP 2FA from Settings. It
// reads status on mount and renders one of three states: enrol (with QR
// + manual secret + first-code confirm), enabled (regenerate codes /
// disable), or — when the server hasn't configured a TOTP key — a muted
// "unavailable" note. Recovery codes are shown exactly once, right after
// they're minted; the server never returns them again.
function TwoFactorCard() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [status, setStatus] = useState<TOTPStatus | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // Enrolment sub-state.
  const [setup, setSetup] = useState<TOTPSetup | null>(null);
  const [confirmCode, setConfirmCode] = useState("");
  // One-time recovery codes display (after confirm or regenerate).
  const [recoveryCodes, setRecoveryCodes] = useState<string[] | null>(null);
  // Disable sub-state.
  const [disabling, setDisabling] = useState(false);
  const [disablePassword, setDisablePassword] = useState("");

  const refresh = useCallback(async () => {
    if (!token) return;
    try {
      setStatus(await api.getTOTPStatus(token));
    } catch (e) {
      if (e instanceof APIError && e.status === 501) setUnavailable(true);
    }
  }, [token]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  if (!token) return null;

  const startEnrol = async () => {
    setErr(null);
    setBusy(true);
    try {
      setSetup(await api.totpSetup(token));
    } catch (e) {
      if (e instanceof APIError && e.status === 503) {
        setUnavailable(true);
      } else {
        setErr((e as Error).message);
      }
    } finally {
      setBusy(false);
    }
  };

  const confirmEnrol = async () => {
    setErr(null);
    setBusy(true);
    try {
      const r = await api.totpConfirm(token, confirmCode.trim());
      setRecoveryCodes(r.recovery_codes);
      setSetup(null);
      setConfirmCode("");
      await refresh();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const regenerate = async () => {
    setErr(null);
    setBusy(true);
    try {
      const r = await api.totpRegenerateRecoveryCodes(token);
      setRecoveryCodes(r.recovery_codes);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const disable = async () => {
    setErr(null);
    setBusy(true);
    try {
      await api.totpDisable(token, disablePassword);
      setDisabling(false);
      setDisablePassword("");
      setRecoveryCodes(null);
      await refresh();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  if (unavailable) {
    return (
      <div className="card settings-card">
        <div className="sf-field">
          <div className="label-row">
            <label>{t("twoFactor.title")}</label>
          </div>
          <div className="desc">{t("twoFactor.unavailable")}</div>
        </div>
      </div>
    );
  }

  return (
    <div className="card settings-card">
      <div className="sf-field">
        <div className="label-row">
          <label>
            <ShieldCheck size={16} style={{ verticalAlign: "-3px" }} />{" "}
            {t("twoFactor.title")}
          </label>
        </div>
        <div className="desc">{t("twoFactor.intro")}</div>

        {/* One-time recovery-code panel — shown after confirm/regenerate. */}
        {recoveryCodes && (
          <div className="totp-recovery">
            <strong>{t("twoFactor.recoveryTitle")}</strong>
            <p className="desc">{t("twoFactor.recoveryWarn")}</p>
            <ul className="totp-recovery-codes">
              {recoveryCodes.map((c) => (
                <li key={c}>
                  <code>{c}</code>
                </li>
              ))}
            </ul>
            <button
              type="button"
              className="secondary"
              onClick={() =>
                void navigator.clipboard?.writeText(recoveryCodes.join("\n"))
              }
            >
              {t("twoFactor.copyCodes")}
            </button>{" "}
            <button
              type="button"
              className="primary"
              onClick={() => setRecoveryCodes(null)}
            >
              {t("twoFactor.savedCodes")}
            </button>
          </div>
        )}

        {/* Enrolment in progress: QR + manual secret + first-code confirm. */}
        {setup && !recoveryCodes && (
          <div className="totp-enrol">
            {setup.qr_png_data_url && (
              <img
                className="totp-qr"
                src={setup.qr_png_data_url}
                alt={t("twoFactor.qrAlt")}
                width={200}
                height={200}
              />
            )}
            <p className="desc">{t("twoFactor.manualSecret")}</p>
            <code className="totp-secret">{setup.secret_base32}</code>
            <label htmlFor="totp-confirm">{t("twoFactor.confirmLabel")}</label>
            <input
              id="totp-confirm"
              type="text"
              inputMode="numeric"
              autoComplete="one-time-code"
              value={confirmCode}
              onChange={(e) => setConfirmCode(e.target.value)}
              placeholder="123456"
            />
            <div>
              <button
                type="button"
                className="primary"
                disabled={busy || !confirmCode.trim()}
                onClick={() => void confirmEnrol()}
              >
                {t("twoFactor.confirmEnable")}
              </button>{" "}
              <button
                type="button"
                className="secondary"
                disabled={busy}
                onClick={() => {
                  setSetup(null);
                  setConfirmCode("");
                  setErr(null);
                }}
              >
                {t("twoFactor.cancel")}
              </button>
            </div>
          </div>
        )}

        {/* Steady state: status + actions, hidden while enrolling. */}
        {!setup && !recoveryCodes && status && (
          <>
            {status.enabled ? (
              <>
                <p className="totp-state totp-on">
                  {t("twoFactor.stateEnabled", {
                    count: status.recovery_codes_left ?? 0,
                  })}
                </p>
                {!disabling ? (
                  <div>
                    <button
                      type="button"
                      className="secondary"
                      disabled={busy}
                      onClick={() => void regenerate()}
                    >
                      {t("twoFactor.regenerate")}
                    </button>{" "}
                    <button
                      type="button"
                      className="danger"
                      disabled={busy}
                      onClick={() => setDisabling(true)}
                    >
                      {t("twoFactor.disable")}
                    </button>
                  </div>
                ) : (
                  <div className="totp-disable">
                    <label htmlFor="totp-disable-pw">
                      {t("twoFactor.disablePasswordLabel")}
                    </label>
                    <input
                      id="totp-disable-pw"
                      type="password"
                      autoComplete="current-password"
                      value={disablePassword}
                      onChange={(e) => setDisablePassword(e.target.value)}
                    />
                    <div>
                      <button
                        type="button"
                        className="danger"
                        disabled={busy || !disablePassword}
                        onClick={() => void disable()}
                      >
                        {t("twoFactor.confirmDisable")}
                      </button>{" "}
                      <button
                        type="button"
                        className="secondary"
                        disabled={busy}
                        onClick={() => {
                          setDisabling(false);
                          setDisablePassword("");
                          setErr(null);
                        }}
                      >
                        {t("twoFactor.cancel")}
                      </button>
                    </div>
                  </div>
                )}
              </>
            ) : (
              <>
                <p className="totp-state totp-off">
                  {t("twoFactor.stateDisabled")}
                </p>
                <button
                  type="button"
                  className="primary"
                  disabled={busy}
                  onClick={() => void startEnrol()}
                >
                  {t("twoFactor.enable")}
                </button>
              </>
            )}
          </>
        )}

        {err && <div className="error">{err}</div>}
      </div>
    </div>
  );
}
