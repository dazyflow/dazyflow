// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  Fragment,
  type ReactNode,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import {
  Search,
  Plus,
  Workflow,
  Activity,
  Table2,
  FolderTree,
  Inbox,
  Boxes,
  Gauge,
  Building2,
  GitBranch,
  KeyRound,
  Lock,
  Mail,
  ScrollText,
  Settings as SettingsIcon,
  ShieldCheck,
  Users,
} from "lucide-react";
import { FlowIcon } from "../icons";
import { useAuth } from "../auth";
import type { FlowSummary } from "../types";

// CommandPalette is the workspace-wide ⌘K launcher: type to jump to any
// page or flow. It deliberately mirrors the editor's step palette (same
// .quick-palette CSS, same keyboard model) so the two feel like one
// affordance in different contexts — AppShell only opens this one OUTSIDE
// the flow editor, where ⌘K stays "Add step".
type Command = {
  id: string;
  label: string;
  sublabel?: string;
  icon: ReactNode;
  group: CommandGroup;
  // keywords are extra search terms that never render — the words someone
  // types when they don't know (or can't recall) what a page is called.
  //
  // They carry BOTH languages in one string rather than living in the
  // translation catalogs. Matching is a substring test, so the active locale
  // is irrelevant: a Swedish user searching "spegling" and an English one
  // searching "mirror" both land on the same row, and neither list has to be
  // kept in sync with the other. Same reasoning as lib/dropSearch's Swedish
  // alias table, which applies in every locale for the same reason.
  keywords?: string;
  run: () => void;
};

// Render order is the order the groups are concatenated in below, and the
// flat keyboard index follows it — so the arrow keys walk the list exactly as
// it reads.
type CommandGroup = "nav" | "admin" | "flow";

export function CommandPalette({
  open,
  onClose,
  flows,
}: {
  open: boolean;
  onClose: () => void;
  flows: FlowSummary[];
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { hasPerm } = useAuth();
  // Same bar the Admin index itself uses: it lists every org card to anyone
  // with either permission and lets each page enforce its own. Mirroring that
  // here keeps the palette and the index showing the same set, instead of the
  // palette inventing a third answer to "what may I reach".
  const canAdmin = hasPerm("organization:admin") || hasPerm("graph:admin");
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  // Reset the query each time the palette opens so it never reappears with a
  // stale search; focus the input and restore focus to the prior element on
  // close (matches the step palette's behaviour).
  useEffect(() => {
    if (!open) return;
    setQuery("");
    setActive(0);
    const prevFocused = document.activeElement as HTMLElement | null;
    inputRef.current?.focus();
    return () => prevFocused?.focus?.();
  }, [open]);

  const go = (path: string) => {
    navigate(path);
    onClose();
  };

  // The full command set: workspace pages, then settings/admin destinations,
  // then one entry per flow.
  const commands = useMemo<Command[]>(() => {
    const nav: Command[] = [
      { id: "nav:new", label: t("flowList.newFlow"), icon: <Plus size={16} />, group: "nav", keywords: "create add nytt skapa", run: () => go("/flows/new") },
      { id: "nav:overview", label: t("nav.overview"), icon: <Gauge size={16} />, group: "nav", keywords: "dashboard home health översikt hem", run: () => go("/overview") },
      { id: "nav:flows", label: t("nav.flows"), icon: <Workflow size={16} />, group: "nav", keywords: "workflows automations flöden", run: () => go("/flows") },
      { id: "nav:runs", label: t("nav.runs"), icon: <Activity size={16} />, group: "nav", keywords: "history logs failures körningar historik loggar", run: () => go("/runs") },
      { id: "nav:results", label: t("nav.results"), icon: <Table2 size={16} />, group: "nav", keywords: "collections tables data resultat tabeller", run: () => go("/collections") },
      { id: "nav:files", label: t("nav.files"), icon: <FolderTree size={16} />, group: "nav", keywords: "uploads storage filer lagring", run: () => go("/files") },
      { id: "nav:approvals", label: t("nav.approvals"), icon: <Inbox size={16} />, group: "nav", keywords: "approve pending inbox godkännanden", run: () => go("/approvals") },
      { id: "nav:apps", label: t("nav.apps"), icon: <Boxes size={16} />, group: "nav", keywords: "integrations connections connectors appar integrationer anslutningar", run: () => go("/apps") },
      { id: "nav:usage", label: t("nav.usage"), icon: <Gauge size={16} />, group: "nav", keywords: "billing quota metering användning fakturering", run: () => go("/usage") },
    ];
    // Settings + admin destinations. These were absent entirely, so anything
    // configured rather than browsed — credentials, secrets, the git mirror —
    // was unreachable from the launcher and effectively undiscoverable.
    const admin: Command[] = [
      { id: "admin:settings", label: t("commandPalette.account"), icon: <SettingsIcon size={16} />, group: "admin", keywords: "profile password 2fa theme language konto lösenord språk tema", run: () => go("/settings") },
    ];
    if (canAdmin) {
      admin.push(
        // The git page hosts both credentials and the workspace mirror, and
        // "back up my flows" is what people actually search for.
        { id: "admin:git", label: t("admin.cardGitTitle"), icon: <GitBranch size={16} />, group: "admin", keywords: "mirror backup sync export deploy key ssh github gitlab spegling spegla säkerhetskopia synk nyckel", run: () => go("/admin/git-credentials") },
        { id: "admin:index", label: t("nav.admin"), icon: <ShieldCheck size={16} />, group: "admin", keywords: "administration organization org inställningar organisation", run: () => go("/admin") },
        { id: "admin:users", label: t("admin.cardUsersTitle"), icon: <Users size={16} />, group: "admin", keywords: "members invite team roles personer bjud in medlemmar roller", run: () => go("/admin/users") },
        { id: "admin:secrets", label: t("admin.cardSecretsTitle"), icon: <Lock size={16} />, group: "admin", keywords: "api keys tokens credentials hemligheter nycklar", run: () => go("/admin/secrets") },
        { id: "admin:apikeys", label: t("admin.cardApiKeysTitle"), icon: <KeyRound size={16} />, group: "admin", keywords: "tokens mcp access api-nycklar", run: () => go("/admin/api-keys") },
        { id: "admin:workspace", label: t("admin.cardWorkspaceTitle"), icon: <Building2 size={16} />, group: "admin", keywords: "organization limits quota name logo organisation gränser namn", run: () => go("/admin/workspace") },
        { id: "admin:audit", label: t("admin.cardAuditTitle"), icon: <ScrollText size={16} />, group: "admin", keywords: "log trail who did what granskningslogg spår", run: () => go("/admin/audit") },
        { id: "admin:sso", label: t("admin.cardSSOTitle"), icon: <ShieldCheck size={16} />, group: "admin", keywords: "saml oidc google login sign-in inloggning", run: () => go("/admin/sso") },
        { id: "admin:emailTemplates", label: t("admin.cardEmailTemplatesTitle"), icon: <Mail size={16} />, group: "admin", keywords: "layout html branding e-postmallar mallar", run: () => go("/admin/email-templates") },
      );
    }
    const flowCmds: Command[] = flows.map((f) => ({
      id: `flow:${f.id}`,
      label: f.name || f.id,
      sublabel: t("commandPalette.flowSublabel"),
      icon: <FlowIcon icon={f.icon} size={16} />,
      group: "flow",
      run: () => go(`/flows/${encodeURIComponent(f.id)}`),
    }));
    return [...nav, ...admin, ...flowCmds];
    // navigate/onClose are stable enough; rebuild when the flow set, the
    // permissions or the locale change.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [flows, t, canAdmin]);

  // Filtered + still grouped (nav before flows) so the flat keyboard index
  // lines up with render order.
  const matches = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return commands;
    return commands.filter(
      (c) =>
        c.label.toLowerCase().includes(q) ||
        (c.sublabel ?? "").toLowerCase().includes(q) ||
        (c.keywords ?? "").includes(q),
    );
  }, [commands, query]);

  // Clamp the active row whenever the result set shrinks.
  useEffect(() => {
    setActive((i) => Math.min(i, Math.max(0, matches.length - 1)));
  }, [matches.length]);

  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      } else if (e.key === "ArrowDown") {
        e.preventDefault();
        setActive((i) => Math.min(matches.length - 1, i + 1));
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        setActive((i) => Math.max(0, i - 1));
      } else if (e.key === "Enter") {
        e.preventDefault();
        matches[active]?.run();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, matches, active, onClose]);

  // Keep the active row visible as arrows move it.
  useLayoutEffect(() => {
    listRef.current
      ?.querySelector<HTMLElement>(`[data-cmd-index="${active}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [active]);

  if (!open) return null;

  // A heading renders wherever the group changes. This replaces the two
  // hardcoded checks ("index 0 is nav", "index of the first flow"), which
  // couldn't express a third group and would have silently dropped the
  // heading for it.
  const headingAt = (i: number): CommandGroup | null => {
    const g = matches[i].group;
    return i === 0 || matches[i - 1].group !== g ? g : null;
  };
  const groupLabel = (g: CommandGroup): string => {
    switch (g) {
      case "nav":
        return t("commandPalette.goTo");
      case "admin":
        return t("commandPalette.settingsGroup");
      case "flow":
        return t("nav.flows");
    }
  };

  return createPortal(
    <div
      className="quick-palette-backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
      role="presentation"
    >
      <div
        className="quick-palette"
        role="dialog"
        aria-modal="true"
        aria-label={t("commandPalette.title")}
      >
        <div className="quick-palette-search">
          <Search size={16} aria-hidden="true" />
          <input
            ref={inputRef}
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("commandPalette.placeholder")}
            aria-label={t("commandPalette.placeholder")}
            spellCheck={false}
            autoComplete="off"
          />
          <span className="quick-palette-count">
            {t("quickPalette.count", { count: matches.length })}
          </span>
        </div>
        <div className="quick-palette-list" ref={listRef}>
          {matches.length === 0 ? (
            <div className="quick-palette-empty">
              {t("commandPalette.noResults", { query: query.trim() })}
            </div>
          ) : (
            matches.map((m, i) => (
              <Fragment key={m.id}>
                {(() => {
                  const heading = headingAt(i);
                  return heading ? (
                    <div className="quick-palette-group">{groupLabel(heading)}</div>
                  ) : null;
                })()}
                <div
                  data-cmd-index={i}
                  role="option"
                  aria-selected={i === active}
                  className={"quick-palette-row" + (i === active ? " active" : "")}
                  onMouseEnter={() => setActive(i)}
                  onMouseDown={(e) => {
                    // mousedown, not click: avoids racing the backdrop's
                    // mousedown-to-close (mirrors the step palette).
                    e.preventDefault();
                    m.run();
                  }}
                >
                  <span className="icon">{m.icon}</span>
                  <span className="quick-palette-row-text">
                    <span className="quick-palette-row-name">{m.label}</span>
                    {m.sublabel && (
                      <span className="quick-palette-row-meta">{m.sublabel}</span>
                    )}
                  </span>
                </div>
              </Fragment>
            ))
          )}
        </div>
        <div className="quick-palette-hint">
          <span>
            <kbd>↑</kbd>
            <kbd>↓</kbd> {t("quickPalette.hintNavigate")}
          </span>
          <span>
            <kbd>{t("quickPalette.enter")}</kbd> {t("commandPalette.hintOpen")}
          </span>
          <span>
            <kbd>Esc</kbd> {t("quickPalette.hintClose")}
          </span>
        </div>
      </div>
    </div>,
    document.body,
  );
}
