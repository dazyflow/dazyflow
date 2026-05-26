import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Moon, Sun } from "lucide-react";
import { applyTheme, getTheme, type ThemeMode } from "../theme";

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
    </div>
  );
}
