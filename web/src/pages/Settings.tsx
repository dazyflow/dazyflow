import { useTranslation } from "react-i18next";

// Settings is the per-user, per-browser preferences page — reached
// from the account menu in the top bar. Today it holds one control:
// the interface language. Language is stored client-side only
// (i18next's localStorage detector, key hazyflow.lang); there's no
// server-side user-locale field yet, so switching here is instant and
// local.
export function Settings() {
  const { t, i18n } = useTranslation();

  const languages = [
    { code: "en", label: t("appSettings.langEnglish") },
    { code: "sv", label: t("appSettings.langSwedish") },
  ];
  // i18n.resolvedLanguage collapses regional codes (sv-SE → sv) to the
  // bundle that's actually active, so the <select> reflects reality.
  const current = i18n.resolvedLanguage ?? i18n.language ?? "en";

  return (
    <div className="page settings-page">
      <h1>{t("appSettings.title")}</h1>
      <p className="page-sub">{t("appSettings.subtitle")}</p>

      <div className="card settings-card">
        <div className="sf-field">
          <div className="label-row">
            <label htmlFor="lang-select">{t("appSettings.languageLabel")}</label>
          </div>
          <select
            id="lang-select"
            value={current}
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
