import { useState } from "react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../auth";
import { api } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { Button } from "./Button";
import { Switch } from "./Switch";
import type { IssuedAPIKey, Permission, Role } from "../types";
import { formatDate } from "../lib/datetime";

// Role templates — common shapes admins reach for. "Custom" disables
// the template effect so the checkbox grid is the source of truth.
// The display name + description are i18n keys (resolved at render
// time) so the picker tracks the active locale.
type Template = {
  id: string;
  nameKey: string;
  descKey: string;
  permissions: Permission[];
};

export const ROLE_TEMPLATES: Template[] = [
  {
    id: "viewer",
    nameKey: "issueKey.templateViewer",
    descKey: "issueKey.templateViewerDesc",
    permissions: ["graph:run"],
  },
  {
    id: "operator",
    nameKey: "issueKey.templateOperator",
    descKey: "issueKey.templateOperatorDesc",
    permissions: ["graph:run", "graph:edit", "secret:read"],
  },
  {
    id: "admin",
    nameKey: "issueKey.templateAdmin",
    descKey: "issueKey.templateAdminDesc",
    permissions: [
      "graph:run",
      "graph:edit",
      "graph:admin",
      "secret:read",
      "secret:write",
      "organization:admin",
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
  "organization:admin",
];

type ExpiryChoice = "never" | "30d" | "90d" | "1y";

const EXPIRY_CHOICES: { id: ExpiryChoice; labelKey: string; days?: number }[] = [
  { id: "never", labelKey: "issueKey.expiryNever" },
  { id: "30d", labelKey: "issueKey.expiry30d", days: 30 },
  { id: "90d", labelKey: "issueKey.expiry90d", days: 90 },
  { id: "1y", labelKey: "issueKey.expiry1y", days: 365 },
];

// expiryToISO converts the dropdown choice into an ISO timestamp the
// daemon parses into time.Time. "never" yields undefined so the
// expires_at field is omitted from the JSON body entirely.
function expiryToISO(choice: ExpiryChoice): string | undefined {
  const entry = EXPIRY_CHOICES.find((c) => c.id === choice);
  if (!entry?.days) return undefined;
  const when = new Date();
  when.setDate(when.getDate() + entry.days);
  return when.toISOString();
}

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
  const { t } = useTranslation();
  const { token, activeTenant } = useAuth();
  const [subject, setSubject] = useState(initialSubject ?? "");
  const [templateID, setTemplateID] = useState<string>("custom");
  const [roleName, setRoleName] = useState("custom");
  const [perms, setPerms] = useState<Set<Permission>>(
    new Set(["graph:run"] as Permission[]),
  );
  // expiry is the dropdown value; "never" omits expires_at in the
  // request entirely (matches the historic operator-default).
  const [expiry, setExpiry] = useState<ExpiryChoice>("never");
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
      onError(t("issueKey.subjectRequired"));
      return;
    }
    if (perms.size === 0) {
      onError(t("issueKey.needAtLeastOnePerm"));
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
        expires_at: expiryToISO(expiry),
      });
      onIssued(issued);
    } catch (e) {
      onError(explainApiError(e, t));
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
          <h2>{t("issueKey.title")}</h2>
        </div>
        <div className="settings-body">
          <div className="sf-field">
            <div className="label-row">
              <label>{t("issueKey.subjectLabel")}</label>
            </div>
            <input
              autoFocus={!initialSubject}
              value={subject}
              onChange={(e) => setSubject(e.target.value)}
              placeholder={t("issueKey.subjectPlaceholder")}
            />
          </div>

          <div className="sf-field">
            <div className="label-row">
              <label>{t("issueKey.roleTemplate")}</label>
            </div>
            <div className="role-template-grid">
              {ROLE_TEMPLATES.map((tpl) => (
                <Button
                  key={tpl.id}
                  className={
                    "role-template" + (templateID === tpl.id ? " active" : "")
                  }
                  onClick={() => applyTemplate(tpl.id)}
                >
                  <div className="role-template-name">{t(tpl.nameKey)}</div>
                  <div className="role-template-desc">{t(tpl.descKey)}</div>
                </Button>
              ))}
              <Button
                className={
                  "role-template" + (templateID === "custom" ? " active" : "")
                }
                onClick={() => applyTemplate("custom")}
              >
                <div className="role-template-name">{t("issueKey.templateCustom")}</div>
                <div className="role-template-desc">
                  {t("issueKey.templateCustomDesc")}
                </div>
              </Button>
            </div>
          </div>

          <div className="sf-field">
            <div className="label-row">
              <label>{t("issueKey.roleName")}</label>
            </div>
            <input
              value={roleName}
              onChange={(e) => setRoleName(e.target.value)}
              placeholder={t("issueKey.rolePlaceholder")}
            />
            <div className="desc">
              {t("issueKey.roleNameDesc")}
            </div>
          </div>

          <div className="sf-field">
            <div className="label-row">
              <label>{t("issueKey.permissions")}</label>
            </div>
            <div className="perm-grid">
              {ALL_PERMISSIONS.map((p) => (
                <Switch
                  key={p}
                  compact
                  checked={perms.has(p)}
                  onChange={() => togglePerm(p)}
                  label={
                    <span style={{ fontFamily: "var(--font-mono)", fontSize: "var(--text-xs)" }}>
                      {p}
                    </span>
                  }
                />
              ))}
            </div>
            <div className="desc">
              <Trans i18nKey="issueKey.tenantAdminWarning" components={[<strong />]} />
            </div>
          </div>

          <div className="sf-field">
            <div className="label-row">
              <label>{t("issueKey.expiryLabel")}</label>
            </div>
            <div className="expiry-grid">
              {EXPIRY_CHOICES.map((c) => (
                <Button
                  key={c.id}
                  className={"expiry-choice" + (expiry === c.id ? " active" : "")}
                  onClick={() => setExpiry(c.id)}
                >
                  {t(c.labelKey)}
                </Button>
              ))}
            </div>
            <div className="desc">
              {expiry === "never"
                ? t("issueKey.expiryNeverDesc")
                : t("issueKey.expirySetDesc", {
                    date: formatDate(expiryToISO(expiry)),
                  })}
            </div>
          </div>
        </div>
        <div className="settings-foot">
          <Button onClick={onCancel}>
            {t("issueKey.cancel")}
          </Button>
          <Button type="submit" variant="primary" disabled={submitting}>
            {submitting ? t("issueKey.issuing") : t("issueKey.issue")}
          </Button>
        </div>
      </form>
    </div>
  );
}
