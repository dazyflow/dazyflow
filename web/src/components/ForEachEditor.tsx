import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { api } from "../api";
import type { Manifest } from "../types";
import type { ReferenceCtx, WorkspaceCtx } from "./SchemaForm";

// ForEachEditor is the non-techie face of the for_each drop. The loop runs
// its BODY — the steps wired to the `body` pin — once per row of the list fed
// into `items`. So instead of a step-module picker, this panel:
//   - tells the user to wire the Loop body pin to the first per-row step,
//   - lists the columns each row exposes (insertable as ${column} in body
//     steps via their "{}" menu), and
//   - exposes the two real knobs: how many rows run at once (concurrency)
//     and whether to stop on the first failing row (fail_fast).
type Props = {
  params: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
  manifests: Manifest[];
  references?: ReferenceCtx;
  workspace?: WorkspaceCtx;
};

export function ForEachEditor({ params, onChange, references }: Props) {
  const { t } = useTranslation();

  // Columns of the list feeding `items`, shown so the user knows what they
  // can reference as ${column} inside the body steps. Deps are the context's
  // primitive fields, not the object itself — the parent recreates the
  // references object every render, so an object dep would refetch per render.
  const [itemFields, setItemFields] = useState<string[]>([]);
  const { token, tenant, workspace: ws, flowId, nodeId } = references ?? {};
  useEffect(() => {
    if (!token || !flowId || !nodeId) return;
    let live = true;
    api
      .listInputFields(token, tenant ?? "", ws ?? "", flowId, nodeId, "items")
      .then((r) => live && setItemFields(r.fields ?? []))
      .catch(() => live && setItemFields((prev) => (prev.length ? [] : prev)));
    return () => {
      live = false;
    };
  }, [token, tenant, ws, flowId, nodeId]);

  const concurrency =
    typeof params.concurrency === "number" ? params.concurrency : undefined;
  const failFast = params.fail_fast === true;

  const setConcurrency = (v: string) => {
    const n = parseInt(v, 10);
    onChange({
      ...params,
      concurrency: Number.isFinite(n) && n > 0 ? n : undefined,
    });
  };
  const setFailFast = (v: boolean) => onChange({ ...params, fail_fast: v });

  return (
    <div className="for-each-editor">
      <div className="hz-loop-banner">{t("forEach.bodyHint")}</div>

      <div className="sf-field">
        <div className="label-row">
          <label>{t("forEach.rowColumns")}</label>
        </div>
        {itemFields.length > 0 ? (
          <div className="for-each-columns">
            {itemFields.map((f) => (
              <code key={f} className="for-each-column">
                {f}
              </code>
            ))}
          </div>
        ) : (
          <div className="for-each-hint">{t("forEach.noColumns")}</div>
        )}
      </div>

      <div className="sf-field">
        <div className="label-row">
          <label>{t("forEach.concurrency")}</label>
        </div>
        <input
          type="number"
          min={1}
          placeholder={t("forEach.concurrencyPlaceholder")}
          value={concurrency ?? ""}
          onChange={(e) => setConcurrency(e.target.value)}
        />
      </div>

      <div className="sf-field">
        <div className="label-row">
          <label>{t("forEach.failFast")}</label>
        </div>
        <select
          value={failFast ? "yes" : "no"}
          onChange={(e) => setFailFast(e.target.value === "yes")}
        >
          <option value="no">{t("forEach.failFastNo")}</option>
          <option value="yes">{t("forEach.failFastYes")}</option>
        </select>
      </div>
    </div>
  );
}
