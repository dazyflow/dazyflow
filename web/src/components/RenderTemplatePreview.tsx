import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { Sparkles } from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import { Button } from "./Button";

// Ready-made starting points. Each sets a working template AND matching
// sample data, so one click gives a non-technical user a rendering email to
// tweak — instead of a blank field that demands Go template syntax.
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

// RenderTemplatePreview shows a live, server-rendered preview of a
// render_template step's HTML as the user edits it — the same engine the
// flow uses at run time, so the preview is exactly what gets sent. It owns
// an editable "sample data" JSON blob (preview input only; the real data is
// wired in at run time) and offers one-click starter layouts.
export function RenderTemplatePreview({
  template,
  onInsertTemplate,
}: {
  template: string;
  onInsertTemplate: (template: string) => void;
}) {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [sample, setSample] = useState<string>(STARTERS[0].sample);
  const [html, setHtml] = useState<string>("");
  const [serverErr, setServerErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const seq = useRef(0);

  // AI assist: describe → generate template.
  const [desc, setDesc] = useState("");
  const [assisting, setAssisting] = useState(false);
  const [assistErr, setAssistErr] = useState<string | null>(null);
  const [needConnect, setNeedConnect] = useState(false);
  // Connected AI providers + the chosen one (remembered per browser, so a
  // user who prefers OpenAI doesn't re-pick every time).
  const [providers, setProviders] = useState<{ name: string; label: string }[]>([]);
  const [provider, setProvider] = useState<string>(
    () => localStorage.getItem("dazyflow.aiProvider") ?? "",
  );

  useEffect(() => {
    if (!token) return;
    let live = true;
    api
      .listLLMProviders(token)
      .then((r) => {
        if (!live) return;
        const list = r.providers ?? [];
        setProviders(list);
        // Keep the remembered choice if it's still connected; else default to
        // the first connected provider.
        setProvider((cur) =>
          list.some((p) => p.name === cur) ? cur : (list[0]?.name ?? ""),
        );
      })
      .catch(() => {
        /* leave empty — generate() will surface need_connect */
      });
    return () => {
      live = false;
    };
  }, [token]);

  // Merge-field names the model may use, taken from the sample data's
  // top-level keys, so generated templates reference fields that exist.
  const fields = useMemo<string[]>(() => {
    try {
      const obj = JSON.parse(sample);
      return obj && typeof obj === "object" && !Array.isArray(obj)
        ? Object.keys(obj)
        : [];
    } catch {
      return [];
    }
  }, [sample]);

  const generate = async () => {
    if (!token || desc.trim() === "" || assisting) return;
    setAssisting(true);
    setAssistErr(null);
    setNeedConnect(false);
    try {
      const r = await api.assistRenderTemplate(token, desc.trim(), fields, provider || undefined);
      if (r.need_connect) setNeedConnect(true);
      else if (r.error) setAssistErr(r.error);
      else if (r.template) onInsertTemplate(r.template);
    } catch (e) {
      setAssistErr((e as Error).message);
    } finally {
      setAssisting(false);
    }
  };

  // Local JSON validity — caught client-side so a half-typed sample doesn't
  // spam the server or look like a template error.
  const jsonError = useMemo(() => {
    if (sample.trim() === "") return null;
    try {
      JSON.parse(sample);
      return null;
    } catch (e) {
      return (e as Error).message;
    }
  }, [sample]);

  const empty = template.trim() === "";

  useEffect(() => {
    if (empty || jsonError || !token) {
      setHtml("");
      setServerErr(null);
      return;
    }
    const id = ++seq.current;
    const handle = setTimeout(() => {
      let data: unknown = {};
      try {
        data = sample.trim() === "" ? {} : JSON.parse(sample);
      } catch {
        return;
      }
      setBusy(true);
      api
        .previewRenderTemplate(token, template, data)
        .then((r) => {
          if (id !== seq.current) return; // a newer keystroke superseded us
          setServerErr(r.error ?? null);
          setHtml(r.error ? "" : (r.html ?? ""));
        })
        .catch((e: Error) => {
          if (id !== seq.current) return;
          setServerErr(e.message);
        })
        .finally(() => {
          if (id === seq.current) setBusy(false);
        });
    }, 350);
    return () => clearTimeout(handle);
  }, [template, sample, jsonError, empty, token]);

  return (
    <div className="rtp">
      <div className="rtp-assist">
        <label className="rtp-label" htmlFor="rtp-desc">
          <Sparkles size={13} style={{ verticalAlign: -2, marginRight: 4 }} />
          {t("renderPreview.assistLabel")}
        </label>
        <div className="rtp-assist-row">
          <input
            id="rtp-desc"
            className="rtp-desc"
            value={desc}
            placeholder={t("renderPreview.assistPlaceholder")}
            onChange={(e) => setDesc(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                void generate();
              }
            }}
          />
          {providers.length > 1 && (
            <select
              className="rtp-provider"
              value={provider}
              aria-label={t("renderPreview.providerLabel")}
              onChange={(e) => {
                setProvider(e.target.value);
                localStorage.setItem("dazyflow.aiProvider", e.target.value);
              }}
            >
              {providers.map((p) => (
                <option key={p.name} value={p.name}>
                  {p.label}
                </option>
              ))}
            </select>
          )}
          <Button
            className="rtp-generate"
            disabled={assisting || desc.trim() === ""}
            onClick={() => void generate()}
          >
            {assisting ? t("renderPreview.generating") : t("renderPreview.generate")}
          </Button>
        </div>
        {needConnect ? (
          <div className="rtp-warn">
            <Trans i18nKey="renderPreview.needConnect" components={[<Link to="/apps?category=ai" />]} />
          </div>
        ) : assistErr ? (
          <div className="rtp-warn">{assistErr}</div>
        ) : null}
      </div>

      <div className="rtp-starters">
        <span className="rtp-label">{t("renderPreview.starters")}</span>
        {STARTERS.map((s) => (
          <Button
            key={s.key}
            className="rtp-starter"
            onClick={() => {
              onInsertTemplate(s.template);
              setSample(s.sample);
            }}
          >
            {t(`renderPreview.starter.${s.key}`)}
          </Button>
        ))}
      </div>

      <label className="rtp-label" htmlFor="rtp-sample">
        {t("renderPreview.sampleData")}
      </label>
      <textarea
        id="rtp-sample"
        className="rtp-sample"
        spellCheck={false}
        value={sample}
        onChange={(e) => setSample(e.target.value)}
        rows={6}
      />
      {jsonError && <div className="rtp-warn">{t("renderPreview.badJson")}</div>}

      <div className="rtp-label rtp-preview-head">
        {t("renderPreview.preview")}
        {busy && <span className="rtp-busy">{t("renderPreview.rendering")}</span>}
      </div>
      {empty ? (
        <div className="rtp-hint">{t("renderPreview.typeToPreview")}</div>
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
    </div>
  );
}
