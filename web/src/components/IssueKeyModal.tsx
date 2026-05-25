import { useState } from "react";
import { useAuth } from "../auth";
import { api } from "../api";
import type { IssuedAPIKey, Permission, Role } from "../types";

// Role templates — common shapes admins reach for. "Custom" disables
// the template effect so the checkbox grid is the source of truth.
// Keeping templates on the frontend is fine for V1; if customer
// deployments end up needing per-tenant overrides, promote to a
// backend `/api/v1/admin/role-templates` endpoint.
type Template = {
  id: string;
  name: string;
  desc: string;
  permissions: Permission[];
};

export const ROLE_TEMPLATES: Template[] = [
  {
    id: "viewer",
    name: "Viewer",
    desc: "Trigger graph runs. No editing, no secrets.",
    permissions: ["graph:run"],
  },
  {
    id: "operator",
    name: "Operator",
    desc: "Run and edit flows. Read secrets.",
    permissions: ["graph:run", "graph:edit", "secret:read"],
  },
  {
    id: "admin",
    name: "Tenant admin",
    desc: "Everything operator can do, plus tenant management (other keys + secrets).",
    permissions: [
      "graph:run",
      "graph:edit",
      "graph:admin",
      "secret:read",
      "secret:write",
      "tenant:admin",
    ],
  },
];

export const ALL_PERMISSIONS: Permission[] = [
  "graph:run",
  "graph:edit",
  "graph:admin",
  "module:register",
  "secret:read",
  "secret:write",
  "tenant:admin",
];

type Props = {
  // Pre-fill the subject when issuing from a user's detail card.
  initialSubject?: string;
  onCancel: () => void;
  onIssued: (issued: IssuedAPIKey) => void;
  onError: (msg: string) => void;
};

export function IssueKeyModal({
  initialSubject,
  onCancel,
  onIssued,
  onError,
}: Props) {
  const { token, activeTenant } = useAuth();
  const [subject, setSubject] = useState(initialSubject ?? "");
  const [templateID, setTemplateID] = useState<string>("custom");
  const [roleName, setRoleName] = useState("custom");
  const [perms, setPerms] = useState<Set<Permission>>(
    new Set(["graph:run"] as Permission[]),
  );
  const [submitting, setSubmitting] = useState(false);

  const applyTemplate = (id: string) => {
    setTemplateID(id);
    if (id === "custom") return;
    const t = ROLE_TEMPLATES.find((x) => x.id === id);
    if (!t) return;
    setPerms(new Set(t.permissions));
    setRoleName(t.id);
  };

  const togglePerm = (p: Permission) =>
    setPerms((s) => {
      const next = new Set(s);
      if (next.has(p)) next.delete(p);
      else next.add(p);
      // Selecting permissions manually pops us off the active template
      // so we don't pretend a custom set is one of the canned roles.
      setTemplateID("custom");
      return next;
    });

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token) return;
    if (!subject.trim()) {
      onError("Subject is required.");
      return;
    }
    if (perms.size === 0) {
      onError("Pick at least one permission.");
      return;
    }
    setSubmitting(true);
    try {
      const role: Role = {
        name: roleName || "custom",
        permissions: Array.from(perms),
      };
      const issued = await api.issueAPIKey(token, {
        subject: subject.trim(),
        // Platform admins working in a switched tenant should issue
        // there, not in their (often empty) own tenant. The backend
        // refuses cross-tenant issuance for non-platform-admins, so
        // sending activeTenant is safe for everyone.
        tenant: activeTenant || undefined,
        roles: [role],
      });
      onIssued(issued);
    } catch (e) {
      onError((e as Error).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="settings-backdrop" onClick={onCancel}>
      <form
        className="settings-dialog"
        style={{ maxWidth: 560 }}
        onClick={(e) => e.stopPropagation()}
        onSubmit={submit}
      >
        <div className="settings-head">
          <h2>Issue API key</h2>
        </div>
        <div className="settings-body">
          <div className="sf-field">
            <div className="label-row">
              <label>Subject (who owns this key) *</label>
            </div>
            <input
              autoFocus={!initialSubject}
              value={subject}
              onChange={(e) => setSubject(e.target.value)}
              placeholder="alice@example.com"
            />
          </div>

          <div className="sf-field">
            <div className="label-row">
              <label>Role template</label>
            </div>
            <div className="role-template-grid">
              {ROLE_TEMPLATES.map((t) => (
                <button
                  key={t.id}
                  type="button"
                  className={
                    "role-template" + (templateID === t.id ? " active" : "")
                  }
                  onClick={() => applyTemplate(t.id)}
                >
                  <div className="role-template-name">{t.name}</div>
                  <div className="role-template-desc">{t.desc}</div>
                </button>
              ))}
              <button
                type="button"
                className={
                  "role-template" + (templateID === "custom" ? " active" : "")
                }
                onClick={() => applyTemplate("custom")}
              >
                <div className="role-template-name">Custom</div>
                <div className="role-template-desc">
                  Pick individual permissions below.
                </div>
              </button>
            </div>
          </div>

          <div className="sf-field">
            <div className="label-row">
              <label>Role name</label>
            </div>
            <input
              value={roleName}
              onChange={(e) => setRoleName(e.target.value)}
              placeholder="custom"
            />
            <div className="desc">
              Cosmetic — shown in the keys list to group similar keys.
            </div>
          </div>

          <div className="sf-field">
            <div className="label-row">
              <label>Permissions *</label>
            </div>
            <div className="perm-grid">
              {ALL_PERMISSIONS.map((p) => (
                <label key={p} className="sf-checkbox">
                  <input
                    type="checkbox"
                    checked={perms.has(p)}
                    onChange={() => togglePerm(p)}
                  />
                  <span style={{ fontFamily: "var(--font-mono)", fontSize: 11 }}>
                    {p}
                  </span>
                </label>
              ))}
            </div>
            <div className="desc">
              <strong>tenant:admin</strong> grants this key the power to
              issue more keys — only assign to fully-trusted operators.
            </div>
          </div>
        </div>
        <div className="settings-foot">
          <button type="button" onClick={onCancel}>
            Cancel
          </button>
          <button type="submit" className="primary" disabled={submitting}>
            {submitting ? "Issuing…" : "Issue"}
          </button>
        </div>
      </form>
    </div>
  );
}
