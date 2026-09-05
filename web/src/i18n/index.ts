// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import i18n from "i18next";
import { initReactI18next } from "react-i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import { loadVocabulary } from "../lib/dropText";
import { primaryLanguage } from "../lib/language";
import en from "./en.json";

// A reader needs ONE catalogue, but every visitor was shipped all of them: the
// two are ~45 KB gzipped each and both sat in the entry chunk, so the sign-in
// page downloaded and parsed the language its reader had not chosen before it
// could paint. Only the FALLBACK is bundled now — it is the one catalogue that
// has to be resident, since it answers any key another language is missing —
// and the rest are code-split, fetched when the reader's language selects them.
const CATALOGUES: Record<string, () => Promise<{ default: object }>> = {
  sv: () => import("./sv.json"),
};

// Resource loading for the fallback stays synchronous (a JSON import bundled
// by Vite) so a reader on it has strings at init, with no fetch in front of
// the first paint. partialBundledLanguages says the store being incomplete is
// expected: another language's catalogue arrives through loadLanguage below.
const initialized = i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: { en: { translation: en } },
    partialBundledLanguages: true,
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
      lookupLocalStorage: "dazyflow.lang",
      caches: ["localStorage"],
    },
    interpolation: {
      // React already escapes; no need for i18next to double-escape.
      escapeValue: false,
    },
    returnNull: false,
  });

// loadLanguage fetches everything `code` needs to render — the UI catalogue
// and the drop vocabulary — and resolves once both are in place. Already
// loaded is a no-op, so a language switched back to costs nothing.
//
// The tag is reduced to its primary subtag first, because that is what
// everything downstream is keyed on: `load: "languageOnly"` above means
// i18next looks a "sv-SE" reader's strings up under "sv", and a bundle added
// under the regional tag would sit there unread while the screen fell through
// to the fallback.
async function loadLanguage(tag: string): Promise<void> {
  const code = primaryLanguage(tag);
  const known = Object.hasOwn(CATALOGUES, code);
  const catalogue =
    known && !i18n.hasResourceBundle(code, "translation")
      ? CATALOGUES[code]().then((m) => {
          i18n.addResourceBundle(code, "translation", m.default);
        })
      : Promise.resolve();
  await Promise.all([catalogue, loadVocabulary(code)]);
}

// setLanguage is the only way the app should switch languages. changeLanguage
// on its own re-renders every consumer the moment it is called — with the new
// language selected and its catalogue not yet fetched, which paints raw
// message keys. Loading first makes the switch atomic.
export async function setLanguage(tag: string): Promise<void> {
  await loadLanguage(tag);
  await i18n.changeLanguage(tag);
}

// i18nReady resolves when the first paint can be trusted to be in the
// reader's language. main.tsx awaits it before rendering: a fetch is not
// instant, and a screen that paints English and then redraws in Swedish is
// worse than one that paints a moment later. Chained off init's own promise,
// because the detector has not run — so there is no language to load — until
// that resolves.
//
// It goes through setLanguage rather than loadLanguage alone for the second
// half of that: i18n.language is the tag the DETECTOR reported, while
// resolvedLanguage is the language i18next found strings for, which at this
// point is still the bundled fallback. Adding a resource bundle does not move
// resolvedLanguage on its own, and half the app reads it — the Settings
// toggle, the tag passed to every drop-text resolver — so the switch has to
// be made explicitly. Nothing has rendered yet, so it costs no redraw.
export const i18nReady: Promise<void> = initialized.then(() =>
  setLanguage(i18n.language ?? i18n.resolvedLanguage ?? "en"),
);

export default i18n;
