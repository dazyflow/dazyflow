// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { ChevronRight, CornerLeftUp, Folder } from "lucide-react";
import { api } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { Button } from "./Button";
import type { FileEntry } from "../types";
import { ErrorNotice } from "./ErrorNotice";

// MoveModal is the interactive "Move to…" folder picker: browse the
// workspace folder tree, then drop the entry into the folder you land on.
// It reuses the files/list endpoint, showing only subfolders. The entry's
// own folder (and any descendant, when moving a folder) is disabled so you
// can't move something into itself or create a cycle.
export function MoveModal({
  entry,
  token,
  tenant,
  workspace,
  onMoved,
  onCancel,
}: {
  entry: FileEntry;
  token: string;
  tenant: string;
  workspace: string;
  onMoved: () => void;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const [dir, setDir] = useState(""); // folder currently being browsed ("" = root)
  const [folders, setFolders] = useState<FileEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [moving, setMoving] = useState(false);

  // The entry's current parent — moving there is a no-op we disable.
  const currentParent = entry.path.includes("/")
    ? entry.path.slice(0, entry.path.lastIndexOf("/"))
    : "";

  // Moving a folder into itself or a descendant would orphan the subtree.
  const intoSelf =
    entry.is_dir && (dir === entry.path || dir.startsWith(entry.path + "/"));
  const isNoop = dir === currentParent;

  const load = useCallback(() => {
    setFolders(null);
    setError(null);
    api
      .listWorkspaceFiles(token, tenant, workspace, dir)
      .then((r) => setFolders((r.entries ?? []).filter((e) => e.is_dir)))
      .catch((e) => setError(explainApiError(e, t)));
  }, [token, tenant, workspace, dir, t]);
  useEffect(load, [load]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onCancel();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onCancel]);

  const doMove = () => {
    setMoving(true);
    setError(null);
    const to = dir ? `${dir}/${entry.name}` : entry.name;
    api
      .renameWorkspaceFile(token, tenant, workspace, entry.path, to)
      .then(onMoved)
      .catch((e) => {
        setError(explainApiError(e, t));
        setMoving(false);
      });
  };

  const segments = dir ? dir.split("/") : [];

  return createPortal(
    <div className="settings-backdrop" onClick={onCancel}>
      <div
        className="settings-dialog move-dialog"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
      >
        <div className="settings-head">
          <h2>{t("files.moveTitle", { name: entry.name })}</h2>
        </div>
        <div className="settings-body">
          <nav className="files-breadcrumb move-breadcrumb">
            <Button variant="link" onClick={() => setDir("")} disabled={dir === ""}>
              {t("files.root")}
            </Button>
            {segments.map((seg, i) => {
              const target = segments.slice(0, i + 1).join("/");
              return (
                <span key={target} className="files-crumb">
                  <ChevronRight size={14} className="files-crumb-sep" />
                  <Button
                    variant="link"
                    onClick={() => setDir(target)}
                    disabled={i === segments.length - 1}
                  >
                    {seg}
                  </Button>
                </span>
              );
            })}
          </nav>

          {error && <ErrorNotice>{error}</ErrorNotice>}

          <div className="move-folder-list">
            {dir !== "" && (
              <Button
                className="move-folder-row"
                onClick={() => setDir(currentParentOf(dir))}
              >
                <CornerLeftUp size={16} className="files-icon" />
                <span className="move-folder-name">{t("files.moveUp")}</span>
              </Button>
            )}
            {folders === null ? (
              <div className="files-empty">{t("files.loading")}</div>
            ) : folders.length === 0 ? (
              <div className="files-empty">{t("files.moveNoSubfolders")}</div>
            ) : (
              folders.map((f) => {
                const disabled =
                  entry.is_dir &&
                  (f.path === entry.path || f.path.startsWith(entry.path + "/"));
                return (
                  <Button
                    key={f.path}
                    className="move-folder-row"
                    disabled={disabled}
                    onClick={() => setDir(f.path)}
                  >
                    <Folder size={16} className="files-icon files-icon-dir" />
                    <span className="move-folder-name">{f.name}</span>
                    <ChevronRight size={16} className="move-folder-chev" />
                  </Button>
                );
              })
            )}
          </div>
        </div>
        <div className="settings-foot">
          <Button type="button" onClick={onCancel}>
            {t("common.cancel")}
          </Button>
          <Button
            type="button"
            variant="primary"
            disabled={moving || intoSelf || isNoop}
            onClick={doMove}
          >
            {isNoop ? t("files.moveAlreadyHere") : t("files.moveHere")}
          </Button>
        </div>
      </div>
    </div>,
    document.body,
  );
}

function currentParentOf(dir: string): string {
  return dir.includes("/") ? dir.slice(0, dir.lastIndexOf("/")) : "";
}
