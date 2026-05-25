import { useState } from "react";
import { Copy } from "lucide-react";
import type { IssuedAPIKey } from "../types";

// RevealSecretModal renders the one-time view of a freshly-minted API
// key's secret. Once closed, the secret is gone — Hazy Flow keeps only
// a salted hash. Used by both the API keys page and the Users page.
export function RevealSecretModal({
  issued,
  onClose,
}: {
  issued: IssuedAPIKey;
  onClose: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(issued.secret);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      /* clipboard may be blocked; user can select + copy manually */
    }
  };
  return (
    <div className="settings-backdrop">
      <div className="settings-dialog" style={{ maxWidth: 540 }}>
        <div className="settings-head">
          <h2>Save this secret — it's shown once</h2>
        </div>
        <div className="settings-body">
          <p className="settings-help">
            The bearer token below is the only chance to see it. Hazy Flow
            stores a salted hash; if you lose this string, revoke the key
            and issue a new one.
          </p>
          <div className="secret-reveal">{issued.secret}</div>
          <button onClick={copy} style={{ marginTop: "var(--space-3)" }}>
            <Copy size={12} style={{ marginRight: 6, verticalAlign: -1 }} />
            {copied ? "Copied" : "Copy to clipboard"}
          </button>
          <div className="sf-field" style={{ marginTop: "var(--space-4)" }}>
            <div className="label-row">
              <label>Key ID</label>
            </div>
            <input
              value={issued.id}
              disabled
              style={{ fontFamily: "var(--font-mono)" }}
            />
          </div>
        </div>
        <div className="settings-foot">
          <button className="primary" onClick={onClose}>
            I've saved it
          </button>
        </div>
      </div>
    </div>
  );
}
