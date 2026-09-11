// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAuth } from "../../auth";
import { api } from "../../api";
import { explainApiError } from "../../lib/explainApiError";

// The rendered half of the template step: the markup sent to the daemon and
// painted back as a picture.
//
// The render happens there rather than here because the template is Go
// html/template — {{range}}, {{if}}, pipelines, contextual escaping — and a
// browser cannot run it. Substituting {{.name}} in JavaScript would agree with
// the real output on the easy cases and diverge silently on the ones people
// actually get wrong, which is worse than no preview at all. The daemon renders
// it through the SAME package the step runs (internal/htmltmpl), so what is on
// screen is what the flow will send.
//
// It has two homes — the inspector's panel and the expanded editor's window —
// and this is the part they share: one debounce, one superseded-response guard,
// one sandbox. Three things that are easy to get subtly wrong twice.
//
// Returns a fragment so each home lays it out itself.
//
// `sample` is the raw JSON text rather than a parsed object on purpose: an
// object parsed by the parent is a new object on every render, and an effect
// keyed on it would render, fetch, re-render and fetch again forever. A string
// compares by value.
// parseSample reads the preview's sample data. Empty text is an empty object,
// not an error: a template with no merge fields previews fine without any.
export function parseSample(text: string): { data: unknown; error: string | null } {
  if (text.trim() === "") return { data: {}, error: null };
  try {
    return { data: JSON.parse(text), error: null };
  } catch (e) {
    return { data: {}, error: (e as Error).message };
  }
}

export function TemplateFrame({
  template,
  sample,
}: {
  template: string;
  sample: string;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [html, setHtml] = useState("");
  const [serverErr, setServerErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const seq = useRef(0);
  const empty = template.trim() === "";
  const dataError = parseSample(sample).error;

  useEffect(() => {
    if (empty || dataError || !token) {
      setHtml("");
      setServerErr(null);
      return;
    }
    const id = ++seq.current;
    // Long enough that a typist is not rendering every letter, short enough
    // that stopping to look costs no wait.
    const handle = setTimeout(() => {
      setBusy(true);
      api
        .previewRenderTemplate(token, template, parseSample(sample).data)
        .then((r) => {
          if (id !== seq.current) return; // a newer keystroke superseded us
          setServerErr(r.error ?? null);
          setHtml(r.error ? "" : (r.html ?? ""));
        })
        .catch((e: unknown) => {
          if (id !== seq.current) return;
          setServerErr(explainApiError(e, t));
        })
        .finally(() => {
          if (id === seq.current) setBusy(false);
        });
    }, 350);
    return () => clearTimeout(handle);
  }, [template, sample, dataError, empty, token, t]);

  return (
    <>
      <div className="rtp-label rtp-preview-head">
        {t("renderPreview.preview")}
        {busy && <span className="rtp-busy">{t("renderPreview.rendering")}</span>}
      </div>
      {empty ? (
        <div className="rtp-hint">{t("renderPreview.typeToPreview")}</div>
      ) : dataError ? (
        <div className="rtp-warn">{t("renderPreview.badJson")}</div>
      ) : serverErr ? (
        <div className="rtp-error">{serverErr}</div>
      ) : (
        // sandbox="" fully neutralizes the preview: no scripts, no forms, no
        // same-origin access — it only paints HTML/CSS, so tenant-authored
        // markup can't run in our origin.
        <iframe
          className="rtp-frame"
          title={t("renderPreview.preview")}
          sandbox=""
          srcDoc={html}
        />
      )}
    </>
  );
}
