// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { Check, Download, Plus, Trash2, X } from "lucide-react";
import { ConfirmModal } from "../ui/ConfirmModal";
import { Button } from "../ui/Button";
import { explainApiError } from "../../lib/explainApiError";
import { ICON } from "../../icons";
import { useEscapeToClose } from "../ui/useEscapeToClose";

// OrgSwitcherModal is the centered org switcher + create flow, mirroring the
// ConfigChecklistModal shell (portal to <body>, modal-backdrop/dialog,
// ESC + backdrop dismiss). It lists the orgs you can act in — click one to
// switch — and offers an inline "Create organization" form. The raw tenant id
// is shown muted under the name only when showId is set (admins/multi-tenant),
// matching the top-bar chip.
export type OrgRow = {
  tenant: string;
  name: string;
  glyph: ReactNode;
  // deletable marks orgs the caller may erase (not their home org; and either
  // platform admin or org admin of the active org). Drives the trash action.
  deletable?: boolean;
};

export function OrgSwitcherModal({
  orgs,
  activeTenant,
  showId,
  onPick,
  onCreate,
  onDelete,
  onExport,
  onClose,
}: {
  orgs: OrgRow[];
  activeTenant: string;
  showId?: boolean;
  onPick: (tenant: string) => void;
  // onCreate creates the org, switches to it, and closes the modal; it
  // rejects with a message we surface inline.
  onCreate: (displayName: string) => Promise<void>;
  // onDelete permanently erases the org (after password step-up), then
  // refreshes/closes. Rejects with a message surfaced in the confirm dialog.
  onDelete: (tenant: string, password: string) => Promise<void>;
  // onExport downloads a copy of the org's data (the export-first step).
  onExport: (tenant: string) => Promise<void>;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<OrgRow | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  // Step-up: the password re-entered to confirm the irreversible delete.
  const [deletePassword, setDeletePassword] = useState("");
  const deletePwRef = useRef<HTMLInputElement>(null);
  // Move focus to the password field when the confirm opens — ConfirmModal
  // autofocuses Cancel, but here we want the user typing the password (so
  // Enter submits the delete rather than dismissing).
  useEffect(() => {
    if (confirmDelete) deletePwRef.current?.focus();
  }, [confirmDelete]);
  // Export-first state for the delete confirm: "idle" → "exporting" → "done".
  const [exportState, setExportState] = useState<"idle" | "exporting" | "done">(
    "idle",
  );

  useEscapeToClose(() => !busy && onClose());

  const submit = async () => {
    const trimmed = name.trim();
    if (!trimmed || busy) return;
    setBusy(true);
    setError(null);
    try {
      await onCreate(trimmed);
      // Parent closes the modal on success; nothing more to do here.
    } catch (e) {
      setError(explainApiError(e, t));
      setBusy(false);
    }
  };

  // submitDelete runs the org deletion (shared by the Delete button and the
  // password field's Enter key). No-ops while busy or without a password.
  const submitDelete = () => {
    if (busy || !confirmDelete) return;
    if (!deletePassword.trim()) {
      setDeleteError(t("nav.orgDeletePasswordRequired"));
      return;
    }
    const target = confirmDelete.tenant;
    setBusy(true);
    setDeleteError(null);
    void onDelete(target, deletePassword)
      .then(() => setConfirmDelete(null)) // parent refreshes / closes
      .catch((e: unknown) => setDeleteError(explainApiError(e, t)))
      .finally(() => setBusy(false));
  };

  return createPortal(
    <div className="modal-backdrop" onClick={() => !busy && onClose()}>
      <div
        className="modal org-switcher-modal"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-labelledby="org-switcher-title"
      >
        <div className="modal-head">
          <h2 id="org-switcher-title">{t("nav.orgsTitle")}</h2>
          <Button
            variant="ghost"
            size="icon"
            onClick={onClose}
            disabled={busy}
            aria-label={t("settings.close")}
          >
            <X size={ICON.lg} />
          </Button>
        </div>
        <div className="modal-body org-switcher-body">
          <ul className="org-switcher-list">
            {orgs.map((org) => {
              const active = org.tenant === activeTenant;
              return (
                <li key={org.tenant} className="org-switcher-row">
                  <Button
                    type="button"
                    className={"org-switcher-item" + (active ? " active" : "")}
                    onClick={() => onPick(org.tenant)}
                    disabled={busy}
                    aria-current={active}
                  >
                    <span className="org-switcher-glyph">{org.glyph}</span>
                    <span className="org-switcher-text">
                      <span className="org-switcher-name">{org.name}</span>
                      {showId && org.name !== org.tenant && (
                        <span className="org-switcher-id">{org.tenant}</span>
                      )}
                    </span>
                    {active && (
                      <Check
                        className="org-switcher-check"
                        size={ICON.md}
                        aria-hidden="true"
                      />
                    )}
                  </Button>
                  {org.deletable && (
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="org-switcher-delete"
                      onClick={() => {
                        setDeleteError(null);
                        setExportState("idle");
                        setDeletePassword("");
                        setConfirmDelete(org);
                      }}
                      disabled={busy}
                      title={t("common.delete")}
                      aria-label={t("nav.orgDeleteAria", { name: org.name })}
                    >
                      <Trash2 size={ICON.sm} />
                    </Button>
                  )}
                </li>
              );
            })}
          </ul>

          {creating ? (
            <div className="org-switcher-create">
              <label htmlFor="org-new-name">{t("nav.orgNameLabel")}</label>
              <input
                id="org-new-name"
                type="text"
                value={name}
                autoFocus
                maxLength={80}
                placeholder={t("nav.orgNamePlaceholder")}
                disabled={busy}
                onChange={(e) => setName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") void submit();
                }}
              />
              {error && <div className="org-switcher-error">{error}</div>}
              <div className="org-switcher-create-actions">
                <Button
                  type="button"
                  onClick={() => {
                    setCreating(false);
                    setName("");
                    setError(null);
                  }}
                  disabled={busy}
                >
                  {t("common.cancel")}
                </Button>
                <Button
                  type="button"
                  variant="primary"
                  onClick={() => void submit()}
                  disabled={busy || !name.trim()}
                >
                  {busy ? t("nav.orgCreating") : t("nav.orgCreate")}
                </Button>
              </div>
            </div>
          ) : (
            <Button
              type="button"
              className="org-switcher-add"
              onClick={() => setCreating(true)}
            >
              <Plus size={ICON.md} />
              <span>{t("nav.orgCreate")}</span>
            </Button>
          )}
        </div>
      </div>
      {confirmDelete && (
        <ConfirmModal
          title={t("nav.orgDeleteTitle", { name: confirmDelete.name })}
          message={
            <>
              {t("nav.orgDeleteBody")}
              {/* Export-first step: download a copy before the irreversible
                  wipe. Offered, not forced — but placed above Delete. */}
              <span className="org-switcher-export">
                <Button
                  type="button"
                  className="org-switcher-export-btn"
                  disabled={busy || exportState === "exporting"}
                  onClick={() => {
                    setExportState("exporting");
                    void onExport(confirmDelete.tenant)
                      .then(() => setExportState("done"))
                      .catch((e: unknown) => {
                        setDeleteError(explainApiError(e, t));
                        setExportState("idle");
                      });
                  }}
                >
                  <Download size={ICON.sm} />
                  {exportState === "exporting"
                    ? t("nav.orgExporting")
                    : exportState === "done"
                      ? t("nav.orgExportAgain")
                      : t("nav.orgExport")}
                </Button>
                {exportState === "done" && (
                  <span className="org-switcher-export-done">
                    <Check size={ICON.sm} /> {t("nav.orgExported")}
                  </span>
                )}
              </span>
              {/* Step-up auth: re-enter password to confirm the wipe. */}
              <span className="org-switcher-pw">
                <label htmlFor="org-delete-pw">{t("nav.orgDeletePassword")}</label>
                <input
                  id="org-delete-pw"
                  ref={deletePwRef}
                  type="password"
                  autoComplete="current-password"
                  value={deletePassword}
                  disabled={busy}
                  placeholder={t("nav.orgDeletePasswordPlaceholder")}
                  onChange={(e) => setDeletePassword(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      submitDelete();
                    }
                  }}
                />
              </span>
              {deleteError && (
                <span className="org-switcher-error">{deleteError}</span>
              )}
            </>
          }
          confirmLabel={busy ? t("nav.orgDeleting") : t("common.delete")}
          cancelLabel={t("common.cancel")}
          danger
          confirmDisabled={busy || !deletePassword.trim()}
          onConfirm={submitDelete}
          onCancel={() => {
            if (!busy) setConfirmDelete(null);
          }}
        />
      )}
    </div>,
    document.body,
  );
}
