import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import en from "./en.json";
import sv from "./sv.json";

// Resource loading is synchronous (JSON imports bundled by Vite) so the
// first render already has the right strings — no flash of fallback
// content. If we add more locales later and they grow large, switch to
// the http-backend loader for code-splitting.
void i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: {
      en: { translation: en },
      sv: { translation: sv },
    },
    fallbackLng: "en",
    // Match "sv-SE", "sv-FI", etc. to "sv" so OS-level regional Swedish
    // locales still resolve to our sv bundle rather than falling
    // through to English.
    load: "languageOnly",
    supportedLngs: ["en", "sv"],
    nonExplicitSupportedLngs: true,
    detection: {
      // Prefer an explicit user choice from localStorage; only fall
      // back to the browser's Accept-Language when nothing is stored.
      order: ["localStorage", "navigator", "htmlTag"],
      lookupLocalStorage: "hazyflow.lang",
      caches: ["localStorage"],
    },
    interpolation: {
      // React already escapes; no need for i18next to double-escape.
      escapeValue: false,
    },
    returnNull: false,
  });

export default i18n;
