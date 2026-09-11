// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import { useTranslation } from "react-i18next";
import i18n from "../../i18n";
import { fieldTitle } from "../../lib/dropText";
import { ExpandedEditor } from "../ui/ExpandedEditor";
import { TemplateFrame, parseSample } from "./TemplateFrame";

type Starter = { key: string; template: string; sample: string };
const STARTERS: Starter[] = [
  {
    key: "greeting",
    template:
      '<div style="font-family:system-ui,sans-serif;color:#16161d">\n  <h2>Hi {{.name}},</h2>\n  <p>{{.message}}</p>\n  <p style="color:#666">— The Team</p>\n</div>',
    sample: JSON.stringify(
      { name: "Alex", message: "Thanks for signing up — you're all set." },
      null,
      2,
    ),
  },
  {
    key: "receipt",
    template:
      '<div style="font-family:system-ui,sans-serif">\n  <h2>Receipt for {{.name}}</h2>\n  <table style="border-collapse:collapse;width:100%">\n    {{range .items}}<tr>\n      <td style="padding:6px;border-bottom:1px solid #eee">{{.qty}}× {{.name}}</td>\n      <td style="padding:6px;border-bottom:1px solid #eee;text-align:right">{{.price}}</td>\n    </tr>{{end}}\n  </table>\n  <p style="text-align:right"><b>Total: {{.total}}</b></p>\n</div>',
    sample: JSON.stringify(
      {
        name: "Alex",
        items: [
          { qty: 2, name: "Widget", price: "$20" },
          { qty: 1, name: "Gadget", price: "$15" },
        ],
        total: "$55",
      },
      null,
      2,
    ),
  },
  {
    key: "announcement",
    template:
      '<div style="font-family:system-ui,sans-serif;text-align:center;padding:24px">\n  <h1>{{.title}}</h1>\n  <p>{{.body}}</p>\n  <p><a href="{{.cta_url}}" style="background:#7f5af0;color:#fff;padding:10px 18px;border-radius:8px;text-decoration:none">{{.cta_label}}</a></p>\n</div>',
    sample: JSON.stringify(
      {
        title: "We've launched 🎉",
        body: "Your new dashboard is ready.",
        cta_url: "https://example.com",
        cta_label: "Take a look",
      },
      null,
      2,
    ),
  },
];

// What a freshly picked layout previews against, so it shows a greeting rather
// than a page of empty fields. Exported because the sample is the inspector's
// to hold — see the props below.
export const DEFAULT_SAMPLE = STARTERS[0].sample;

export function RenderTemplatePreview({
  template,
  sample,
  onSampleChange,
  onInsertTemplate,
}: {
  template: string;
  // The sample data belongs to the caller rather than to this panel: the
  // expanded editor shows the same preview beside the markup, and test data
  // that reset itself the moment a window opened would be data typed twice.
  sample: string;
  onSampleChange: (sample: string) => void;
  onInsertTemplate: (template: string) => void;
}) {
  const { t } = useTranslation();
  const [starter, setStarter] = useState<string>("");
  const jsonError = parseSample(sample).error;

  return (
    <div className="rtp">
      <div className="rtp-head">
        <label className="rtp-label" htmlFor="rtp-starter">
          {t("renderPreview.starters")}
        </label>
        {/* The markup itself lives behind the Advanced disclosure below, which
            is the right place for it and a long way from here. This opens the
            same field as a window, with this same preview beside it. */}
        <ExpandedEditor
          title={fieldTitle("Template", i18n.language)}
          value={template}
          lang="html"
          onChange={onInsertTemplate}
          preview={<TemplateFrame template={template} sample={sample} />}
        />
      </div>
      <select
        id="rtp-starter"
        className="rtp-type"
        value={starter}
        onChange={(e) => {
          const key = e.target.value;
          setStarter(key);
          const s = STARTERS.find((x) => x.key === key);
          if (s) {
            onInsertTemplate(s.template);
            onSampleChange(s.sample);
          }
        }}
      >
        <option value="">{t("renderPreview.choose")}</option>
        {STARTERS.map((s) => (
          <option key={s.key} value={s.key}>
            {t(`renderPreview.starter.${s.key}`)}
          </option>
        ))}
      </select>

      <label className="rtp-label" htmlFor="rtp-sample">
        {t("renderPreview.sampleData")}
      </label>
      <textarea
        id="rtp-sample"
        className="rtp-sample"
        spellCheck={false}
        value={sample}
        onChange={(e) => onSampleChange(e.target.value)}
        rows={6}
      />
      {jsonError && <div className="rtp-warn">{t("renderPreview.badJson")}</div>}

      <TemplateFrame template={template} sample={sample} />
    </div>
  );
}
