// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Bell, Monitor, Moon, ShieldCheck, Sun } from "lucide-react";
import { applyTheme, getThemeMode, type ThemeMode } from "../theme";
import { useAuth } from "../auth";
import { api, APIError, type TOTPSetup, type TOTPStatus } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { OtpInput } from "../components/OtpInput";
import { Switch } from "../components/Switch";
import { Button } from "../components/Button";

// Settings is the per-user, per-browser preferences page — reached
// from the account menu in the sidebar. Holds appearance + language,
// both stored client-side only (localStorage); there are no
// server-side user prefs yet, so switching here is instant and local.
export function Settings() {
  const { t, i18n } = useTranslation();
  const { token } = useAuth();

  const languages = [
    { code: "en", label: t("appSettings.langEnglish") },
    { code: "sv", label: t("appSettings.langSwedish") },
  ];
  // i18n.resolvedLanguage collapses regional codes (sv-SE → sv) to the
  // bundle that's actually active, so the <select> reflects reality.
  const currentLang = i18n.resolvedLanguage ?? i18n.language ?? "en";

  // Theme is applied imperatively (data-theme on <html>); keep a local
  // mirror just to drive the selected-state on the three cards. This
  // mirrors the user's CHOICE ("system" included), not the resolved
  // dark/light — otherwise picking System would light up whichever of
  // Dark/Light the OS happens to be on.
  const [theme, setTheme] = useState<ThemeMode>(getThemeMode());
  const pickTheme = (mode: ThemeMode) => {
    // Apply locally first (instant, and refreshes the localStorage boot
    // cache), then persist to the account so the choice roams to other
    // devices. The server write is best-effort: a failure leaves the
    // local change in place — the next change or login reconciles it.
    applyTheme(mode);
    setTheme(mode);
    if (token) void api.updatePreferences(token, { theme: mode }).catch(() => {});
  };
  const pickLanguage = (code: string) => {
    // changeLanguage swaps the active catalogue AND, via the
    // languagedetector's localStorage cache, persists the choice locally
    // so it survives reloads. Mirror it to the account so the locale
    // roams; best-effort, same contract as the theme write.
    void i18n.changeLanguage(code);
    if (token) void api.updatePreferences(token, { language: code }).catch(() => {});
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
            {/* System first, and the default: it's the option that needs no
                decision from the user, so it leads. */}
            <button
              type="button"
              className={"theme-option" + (theme === "system" ? " active" : "")}
              aria-pressed={theme === "system"}
              onClick={() => pickTheme("system")}
            >
              <span className="theme-swatch theme-swatch-system" aria-hidden="true">
                <Monitor size={16} />
              </span>
              <span className="theme-option-label">{t("appSettings.themeSystem")}</span>
            </button>
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
            onChange={(e) => pickLanguage(e.target.value)}
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

      <NotificationsCard />
      <TwoFactorCard />
    </div>
  );
}

// NotificationsCard manages the signed-in user's account-level
// notification preferences. Unlike the appearance/language cards above
// (which are browser-local), these persist server-side via
// /me/preferences, so they follow the account across devices. Loads on
// mount and saves each toggle immediately; a failed save reverts the
// switch so the UI never claims a setting that didn't stick.
function NotificationsCard() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [emailOnFailure, setEmailOnFailureState] = useState<boolean | null>(null);
  const [emailOnSupport, setEmailOnSupportState] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    api
      .getPreferences(token)
      .then((p) => {
        if (cancelled) return;
        setEmailOnFailureState(p.email_on_flow_failure);
        setEmailOnSupportState(p.email_on_support_reply);
      })
      .catch((e) => {
        if (!cancelled) setErr(explainApiError(e, t));
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  if (!token) return null;

  const setEmailOnFailure = async (next: boolean) => {
    if (emailOnFailure === null || busy) return;
    const prev = emailOnFailure;
    // Optimistic flip so the toggle feels instant; revert on failure.
    // Partial PUT — only this field is sent, so the theme/language prefs
    // the user may also have set are left untouched.
    setEmailOnFailureState(next);
    setErr(null);
    setBusy(true);
    try {
      const saved = await api.updatePreferences(token, {
        email_on_flow_failure: next,
      });
      setEmailOnFailureState(saved.email_on_flow_failure);
    } catch (e) {
      setEmailOnFailureState(prev);
      setErr(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  };

  // Same optimistic-flip + partial-PUT contract as the failure toggle; kept
  // separate so turning one off never implicitly writes the other.
  const setEmailOnSupport = async (next: boolean) => {
    if (emailOnSupport === null || busy) return;
    const prev = emailOnSupport;
    setEmailOnSupportState(next);
    setErr(null);
    setBusy(true);
    try {
      const saved = await api.updatePreferences(token, {
        email_on_support_reply: next,
      });
      setEmailOnSupportState(saved.email_on_support_reply);
    } catch (e) {
      setEmailOnSupportState(prev);
      setErr(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="card settings-card">
      <div className="sf-field">
        <div className="label-row">
          <label>
            <Bell size={16} style={{ verticalAlign: "-3px" }} />{" "}
            {t("notifications.title")}
          </label>
        </div>
        <div className="desc">{t("notifications.intro")}</div>
        {emailOnFailure !== null && (
          <Switch
            checked={emailOnFailure}
            onChange={(v) => void setEmailOnFailure(v)}
            disabled={busy}
            label={t("notifications.flowFailureLabel")}
            description={t("notifications.flowFailureDesc")}
          />
        )}
        {emailOnSupport !== null && (
          <Switch
            checked={emailOnSupport}
            onChange={(v) => void setEmailOnSupport(v)}
            disabled={busy}
            label={t("notifications.supportReplyLabel")}
            description={t("notifications.supportReplyDesc")}
          />
        )}
        {err && <div className="error">{err}</div>}
      </div>
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
  // Transient "Copied" affordance for the copy buttons.
  const [copied, setCopied] = useState<"secret" | "codes" | null>(null);

  const copy = (text: string, which: "secret" | "codes") => {
    void navigator.clipboard?.writeText(text);
    setCopied(which);
    window.setTimeout(() => setCopied((c) => (c === which ? null : c)), 1500);
  };

  // Recovery codes are shown once — let the user keep an offline copy as a
  // .txt instead of only the clipboard (which a later copy would clobber).
  const downloadCodes = (codes: string[]) => {
    const blob = new Blob([codes.join("\n") + "\n"], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "dazyflow-recovery-codes.txt";
    a.click();
    URL.revokeObjectURL(url);
  };

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
        setErr(explainApiError(e, t));
      }
    } finally {
      setBusy(false);
    }
  };

  const confirmEnrol = async (codeOverride?: string) => {
    const code = (codeOverride ?? confirmCode).trim();
    if (code.length < 6 || busy) return;
    setErr(null);
    setBusy(true);
    try {
      const r = await api.totpConfirm(token, code);
      setRecoveryCodes(r.recovery_codes);
      setSetup(null);
      setConfirmCode("");
      await refresh();
    } catch (e) {
      setErr(explainApiError(e, t));
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
      setErr(explainApiError(e, t));
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
      setErr(explainApiError(e, t));
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
            <div className="totp-actions">
              <Button
                onClick={() => copy(recoveryCodes.join("\n"), "codes")}
              >
                {copied === "codes" ? t("twoFactor.copied") : t("twoFactor.copyCodes")}
              </Button>
              <Button
                onClick={() => downloadCodes(recoveryCodes)}
              >
                {t("twoFactor.downloadCodes")}
              </Button>
              <Button
                variant="primary"
                onClick={() => setRecoveryCodes(null)}
              >
                {t("twoFactor.savedCodes")}
              </Button>
            </div>
          </div>
        )}

        {/* Enrolment in progress: QR + manual secret + first-code confirm. */}
        {setup && !recoveryCodes && (
          <div className="totp-enrol">
            <div className="totp-qr-row">
              {setup.qr_png_data_url && (
                <img
                  className="totp-qr"
                  src={setup.qr_png_data_url}
                  alt={t("twoFactor.qrAlt")}
                  width={200}
                  height={200}
                />
              )}
              <div className="totp-manual">
                <p className="desc">{t("twoFactor.manualSecret")}</p>
                <div className="totp-secret-row">
                  <code className="totp-secret">{setup.secret_base32}</code>
                  <Button
                    className="linklike"
                    onClick={() => copy(setup.secret_base32, "secret")}
                  >
                    {copied === "secret" ? t("twoFactor.copied") : t("twoFactor.copySecret")}
                  </Button>
                </div>
              </div>
            </div>
            <label>{t("twoFactor.confirmLabel")}</label>
            <OtpInput
              value={confirmCode}
              onChange={setConfirmCode}
              onComplete={(v) => void confirmEnrol(v)}
              disabled={busy}
              autoFocus
              ariaLabel={t("twoFactor.confirmLabel")}
            />
            <div className="totp-actions">
              <Button
                variant="primary"
                disabled={busy || confirmCode.trim().length < 6}
                onClick={() => void confirmEnrol()}
              >
                {t("twoFactor.confirmEnable")}
              </Button>
              <Button
                disabled={busy}
                onClick={() => {
                  setSetup(null);
                  setConfirmCode("");
                  setErr(null);
                }}
              >
                {t("twoFactor.cancel")}
              </Button>
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
                    <Button
                      disabled={busy}
                      onClick={() => void regenerate()}
                    >
                      {t("twoFactor.regenerate")}
                    </Button>{" "}
                    <Button
                      variant="danger"
                      disabled={busy}
                      onClick={() => setDisabling(true)}
                    >
                      {t("twoFactor.disable")}
                    </Button>
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
                      <Button
                        variant="danger"
                        disabled={busy || !disablePassword}
                        onClick={() => void disable()}
                      >
                        {t("twoFactor.confirmDisable")}
                      </Button>{" "}
                      <Button
                        disabled={busy}
                        onClick={() => {
                          setDisabling(false);
                          setDisablePassword("");
                          setErr(null);
                        }}
                      >
                        {t("twoFactor.cancel")}
                      </Button>
                    </div>
                  </div>
                )}
              </>
            ) : (
              <>
                <p className="totp-state totp-off">
                  {t("twoFactor.stateDisabled")}
                </p>
                <Button
                  variant="primary"
                  disabled={busy}
                  onClick={() => void startEnrol()}
                >
                  {t("twoFactor.enable")}
                </Button>
              </>
            )}
          </>
        )}

        {err && <div className="error">{err}</div>}
      </div>
    </div>
  );
}
