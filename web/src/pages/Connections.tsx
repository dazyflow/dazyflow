import { useSearchParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Apps } from "./Apps";
import { Secrets } from "./Secrets";

// Connections unifies the two credential surfaces that used to be
// separate top-level nav items ("Apps" + "Secrets") under one
// destination. Apps is the integration catalog (connect Slack/Gmail,
// see what's ready to use); Secrets is the raw ${secret.NAME} vault.
// They overlap heavily — both answer "what's connected / what do I
// still need to set up?" and both read the same secret store — so a
// single page with two tabs is far less confusing than two siblings
// whose names didn't make the distinction obvious.
//
// The active tab lives in ?tab= so deep links land on the right one,
// and the editor's "set up this credential" link (/secrets?focus=NAME,
// redirected here) still reaches the Secrets tab with its focus param
// intact — Secrets reads ?focus= via its own useSearchParams.
export function Connections() {
  const { t } = useTranslation();
  const [params, setParams] = useSearchParams();
  const tab = params.get("tab") === "secrets" ? "secrets" : "apps";
  const setTab = (next: "apps" | "secrets") => {
    const p = new URLSearchParams(params);
    p.set("tab", next);
    // `focus` only applies to the Secrets tab; drop it when leaving so a
    // stale highlight doesn't re-fire on the Apps tab.
    if (next !== "secrets") p.delete("focus");
    setParams(p, { replace: true });
  };
  return (
    <div className="connections-hub">
      <div className="connections-tabs" role="tablist">
        <button
          type="button"
          role="tab"
          aria-selected={tab === "apps"}
          className={"connections-tab" + (tab === "apps" ? " active" : "")}
          onClick={() => setTab("apps")}
        >
          {t("connections.tabApps")}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === "secrets"}
          className={"connections-tab" + (tab === "secrets" ? " active" : "")}
          onClick={() => setTab("secrets")}
        >
          {t("connections.tabSecrets")}
        </button>
      </div>
      {tab === "apps" ? <Apps /> : <Secrets />}
    </div>
  );
}
