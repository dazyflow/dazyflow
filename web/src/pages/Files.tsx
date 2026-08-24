// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  ChevronRight,
  Download,
  File as FileIcon,
  Folder,
  FolderInput,
  FolderPlus,
  Pencil,
  Trash2,
  Upload,
} from "lucide-react";
import { api } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { useAuth } from "../auth";
import { ConfirmModal } from "../components/ui/ConfirmModal";
import { Button } from "../components/ui/Button";
import { Switch } from "../components/ui/Switch";
import { PromptModal } from "../components/ui/PromptModal";
import { MoveModal } from "../components/dialogs/MoveModal";
import { useUploads } from "../uploads";
import type { FileEntry } from "../types";
import { ErrorNotice } from "../components/ui/ErrorNotice";
import { ICON } from "../icons";
import { formatBytes } from "../lib/format";

// Files is the workspace file manager: a browsable view of the persistent
// sandbox that flows read from and write to (git_checkout clones, file_write
// outputs, uploads). It's an authoring surface — both viewing and mutating
// require graph:edit, so it's gated to editors/admins (viewers, who can only
// run flows, don't see it). Mutations are additionally enforced server-side.


function joinPath(dir: string, name: string): string {
  return dir ? `${dir}/${name}` : name;
}

// isHidden marks dotfiles/dot-directories — including the workspace's
// internal .dazyflow-store (the drops/results DB) — which clutter the
// listing and are rarely edited by hand. They stay in the response but are
// filtered out of the view unless the user opts in via the toggle.
function isHidden(entry: FileEntry): boolean {
  return entry.name.startsWith(".");
}

// Remember the show-hidden choice across navigation and reloads, like the
// theme/language settings — it's a per-browser view preference, not data.
const SHOW_HIDDEN_KEY = "dz.files.showHidden";

export function Files() {
  const { t } = useTranslation();
  const { token, activeTenant, activeWorkspace, hasPerm } = useAuth();
  const uploads = useUploads();
  const canWrite = hasPerm("graph:edit");

  // cwd is the current directory relative to the workspace root ("" = root).
  const [cwd, setCwd] = useState("");
  const [entries, setEntries] = useState<FileEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [usage, setUsage] = useState<{ used: number; limit: number } | null>(null);
  const [busy, setBusy] = useState(false);
  const fileInput = useRef<HTMLInputElement>(null);

  // showHidden reveals dotfiles (off by default so .dazyflow-store et al.
  // don't clutter the listing). Persisted per-browser.
  const [showHidden, setShowHidden] = useState(
    () => localStorage.getItem(SHOW_HIDDEN_KEY) === "1",
  );
  const toggleHidden = (v: boolean) => {
    setShowHidden(v);
    localStorage.setItem(SHOW_HIDDEN_KEY, v ? "1" : "0");
  };

  // Pending modal actions (themed replacements for prompt/confirm).
  const [creatingFolder, setCreatingFolder] = useState(false);
  const [renaming, setRenaming] = useState<FileEntry | null>(null);
  const [deleting, setDeleting] = useState<FileEntry | null>(null);
  const [movingPick, setMovingPick] = useState<FileEntry | null>(null);

  // Drag-and-drop move state. dragSrc is the entry being dragged; dropTarget
  // is the directory path currently hovered ("" = workspace root, null =
  // none) so the target can highlight.
  const [dragSrc, setDragSrc] = useState<FileEntry | null>(null);
  const [dropTarget, setDropTarget] = useState<string | null>(null);
  // springing holds the folder path mid-spring so it can flash before the
  // view navigates into it.
  const [springing, setSpringing] = useState<string | null>(null);
  // fileDragOver is true while OS files are dragged over the page (distinct
  // from the internal move drag), so we can show a "drop to upload" overlay.
  const [fileDragOver, setFileDragOver] = useState(false);

  const ready = !!token && !!activeTenant && !!activeWorkspace;

  const refresh = useCallback(() => {
    // Files is editor-gated; viewers can't read it, so skip the (403-bound)
    // fetch entirely — the page renders a no-access notice instead.
    if (!ready || !canWrite) return;
    setError(null);
    setEntries(null);
    api
      .listWorkspaceFiles(token!, activeTenant, activeWorkspace, cwd)
      .then((r) => setEntries(r.entries ?? []))
      .catch((e) =>
        setError(explainApiError(e, t)),
      );
    api
      .workspaceFileUsage(token!, activeTenant, activeWorkspace)
      .then(setUsage)
      .catch(() => setUsage(null));
  }, [ready, canWrite, token, activeTenant, activeWorkspace, cwd]);

  useEffect(refresh, [refresh]);

  const guard = async (fn: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    try {
      await fn();
      refresh();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(false);
    }
  };

  const onDownload = async (entry: FileEntry) => {
    try {
      const blob = await api.downloadWorkspaceFile(
        token!,
        activeTenant,
        activeWorkspace,
        entry.path,
      );
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = entry.name;
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(url);
    } catch (e) {
      setError(explainApiError(e, t));
    }
  };

  // Uploads run through the app-level uploader: they keep going (and show
  // progress in the global indicator) across page navigation, accept
  // multiple files, and report per-file progress. The view refreshes when
  // one targeting the current folder finishes (see the effect below).
  const onUpload = (files: FileList | null) => {
    if (!files || files.length === 0) return;
    uploads.enqueue(Array.from(files), {
      token: token!,
      tenant: activeTenant,
      workspace: activeWorkspace,
      dir: cwd,
    });
  };

  // Refresh when a background upload into the folder we're viewing completes.
  useEffect(() => {
    const d = uploads.lastDone;
    if (d && d.tenant === activeTenant && d.workspace === activeWorkspace && d.dir === cwd) {
      refresh();
    }
    // Keyed on completedNonce: fires once per finished upload, reading the
    // latest cwd/refresh from this render's closure.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [uploads.completedNonce]);

  const submitNewFolder = (name: string) => {
    setCreatingFolder(false);
    guard(() =>
      api.mkdirWorkspaceDir(token!, activeTenant, activeWorkspace, joinPath(cwd, name)),
    );
  };

  // The rename modal edits the file's full workspace-relative path, so it
  // doubles as "move": changing only the last segment renames in place,
  // while adding/altering a folder prefix moves it (the API creates the
  // destination's parent folders as needed).
  const submitRename = (entry: FileEntry, nextPath: string) => {
    setRenaming(null);
    if (nextPath === entry.path) return;
    guard(() =>
      api.renameWorkspaceFile(token!, activeTenant, activeWorkspace, entry.path, nextPath),
    );
  };

  const confirmDelete = (entry: FileEntry) => {
    setDeleting(null);
    guard(() =>
      api.deleteWorkspaceFile(token!, activeTenant, activeWorkspace, entry.path),
    );
  };

  // canDrop rejects no-op moves (already in targetDir) and moving a folder
  // into itself or a descendant (which would orphan the subtree).
  const canDrop = (entry: FileEntry, targetDir: string): boolean => {
    if (joinPath(targetDir, entry.name) === entry.path) return false;
    if (entry.is_dir && (targetDir === entry.path || targetDir.startsWith(entry.path + "/")))
      return false;
    return true;
  };

  const moveTo = (entry: FileEntry, targetDir: string) => {
    if (!canDrop(entry, targetDir)) return;
    guard(() =>
      api.renameWorkspaceFile(
        token!,
        activeTenant,
        activeWorkspace,
        entry.path,
        joinPath(targetDir, entry.name),
      ),
    );
  };

  // Wire one directory (a folder row or a breadcrumb crumb) as a drop
  // target during a drag. While a drag is active, every valid destination
  // lights up (data-droppable) so it's clear where the item can go; the one
  // under the cursor lights up more strongly (data-drop).
  const dropProps = (targetDir: string) => {
    if (!canWrite) return {};
    const droppable = !!dragSrc && canDrop(dragSrc, targetDir);
    return {
      onDragOver: (e: React.DragEvent) => {
        if (droppable) {
          e.preventDefault();
          // Stop the event reaching an enclosing drop target (the list area
          // is a drop target for the current folder), so hovering a
          // subfolder row highlights the row, not the whole list.
          e.stopPropagation();
          e.dataTransfer.dropEffect = "move";
          if (dropTarget !== targetDir) setDropTarget(targetDir);
        }
      },
      onDragLeave: (e: React.DragEvent) => {
        // Ignore dragleave that just crossed into a child element of the
        // same target — otherwise the highlight (and the spring timer keyed
        // on it) would flicker as the cursor moves over the row's cells.
        if (e.currentTarget.contains(e.relatedTarget as Node)) return;
        setDropTarget((cur) => (cur === targetDir ? null : cur));
      },
      onDrop: (e: React.DragEvent) => {
        e.preventDefault();
        e.stopPropagation();
        if (dragSrc) moveTo(dragSrc, targetDir);
        setDragSrc(null);
        setDropTarget(null);
      },
      "data-droppable": droppable ? "true" : undefined,
      "data-drop": dropTarget === targetDir ? "true" : undefined,
      "data-springing": springing === targetDir ? "true" : undefined,
    };
  };

  // Spring-loaded folders: hovering a dragged item over a folder (or a
  // breadcrumb crumb) for ~0.7s opens it, so you can drill into a nested
  // destination mid-drag without dropping first. Keyed on the hovered
  // target — moving away or dropping cancels via the cleanup.
  useEffect(() => {
    if (!dragSrc || dropTarget === null || dropTarget === cwd) return;
    const target = dropTarget;
    let openId: number | undefined;
    // After the hover threshold, flash the folder briefly, then open it.
    const pulseId = window.setTimeout(() => {
      setSpringing(target);
      openId = window.setTimeout(() => {
        setSpringing(null);
        setDropTarget(null);
        setCwd(target);
      }, 260);
    }, 700);
    return () => {
      window.clearTimeout(pulseId);
      if (openId) window.clearTimeout(openId);
      setSpringing((s) => (s === target ? null : s));
    };
  }, [dragSrc, dropTarget, cwd]);

  // Breadcrumb segments from cwd; clicking one navigates up to it.
  const segments = cwd ? cwd.split("/") : [];

  // The rows actually shown: dotfiles are filtered out unless showHidden is
  // on. hiddenCount drives the "only hidden files here" hint so an
  // all-dotfile folder doesn't look misleadingly empty. null while loading.
  const visible = entries?.filter((e) => showHidden || !isHidden(e)) ?? null;
  const hiddenCount = entries ? entries.length - (visible?.length ?? 0) : 0;

  const quotaPct =
    usage && usage.limit > 0 ? Math.min(100, (usage.used / usage.limit) * 100) : 0;

  // Dragging files from the OS onto the page uploads them into the current
  // folder — a picker-free path that works even where the native file
  // dialog is suppressed. We detect an *external* (OS file) drag as "a drag
  // with no internal dragSrc set". This avoids sniffing
  // dataTransfer.types.includes("Files"), which is unreliable across
  // browsers (Firefox in particular), and it's what broke drop there: the
  // type check failed, so we never preventDefault'd and Firefox ignored the
  // drop. An internal move always sets dragSrc, so the two never collide.
  const externalDrag = () => canWrite && dragSrc === null;

  // Editor-gated surface: a viewer (graph:run only) who navigates here directly
  // gets a notice rather than an empty/403 manager.
  if (!canWrite) {
    return (
      <div className="page files-page">
        <h1>{t("files.title")}</h1>
        <p className="page-sub">{t("files.noAccess")}</p>
      </div>
    );
  }

  return (
    <div
      className="page files-page"
      data-filedrag={fileDragOver ? "true" : undefined}
      onDragEnter={(e) => {
        // Both dragenter and dragover must preventDefault for a drop to be
        // accepted (Firefox is strict about this).
        if (externalDrag()) {
          e.preventDefault();
          if (!fileDragOver) setFileDragOver(true);
        }
      }}
      onDragOver={(e) => {
        if (externalDrag()) {
          e.preventDefault();
          if (!fileDragOver) setFileDragOver(true);
        }
      }}
      onDragLeave={(e) => {
        // Only clear when the drag actually leaves the page, not on moves
        // between children.
        if (!e.currentTarget.contains(e.relatedTarget as Node)) setFileDragOver(false);
      }}
      onDrop={(e) => {
        if (externalDrag()) {
          // preventDefault unconditionally for external drags so the browser
          // never falls back to opening/navigating to the dropped file.
          e.preventDefault();
          const files = e.dataTransfer.files;
          if (files && files.length > 0) {
            uploads.enqueue(Array.from(files), {
              token: token!,
              tenant: activeTenant,
              workspace: activeWorkspace,
              dir: cwd,
            });
          }
        }
        setFileDragOver(false);
      }}
    >
      {fileDragOver && (
        <div className="files-dropzone-overlay">
          <Upload size={28} />
          <span>{t("files.dropToUpload", { folder: cwd || t("files.root") })}</span>
        </div>
      )}
      <div className="page-title">
        <h1>{t("files.title")}</h1>
      </div>

      <div className="files-toolbar">
        <nav className="files-breadcrumb" aria-label={t("files.breadcrumbLabel")}>
          <Button
            variant="link"
            onClick={() => setCwd("")}
            disabled={cwd === ""}
            {...dropProps("")}
          >
            {t("files.root")}
          </Button>
          {segments.map((seg, i) => {
            const target = segments.slice(0, i + 1).join("/");
            return (
              <span key={target} className="files-crumb">
                <ChevronRight size={ICON.sm} className="files-crumb-sep" />
                <Button
                  variant="link"
                  onClick={() => setCwd(target)}
                  disabled={i === segments.length - 1}
                  {...dropProps(target)}
                >
                  {seg}
                </Button>
              </span>
            );
          })}
        </nav>

        {canWrite && (
          <div className="files-actions">
            <Switch
              checked={showHidden}
              onChange={toggleHidden}
              label={t("files.showHidden")}
              compact
            />
            <Button
              variant="ghost"
              onClick={() => setCreatingFolder(true)}
              disabled={busy}
            >
              <FolderPlus size={ICON.md} /> {t("files.newFolder")}
            </Button>
            <Button
              variant="ghost"
              onClick={() => fileInput.current?.click()}
              disabled={busy}
            >
              <Upload size={ICON.md} /> {t("files.upload")}
            </Button>
            <input
              ref={fileInput}
              type="file"
              multiple
              hidden
              onChange={(e) => {
                onUpload(e.target.files);
                e.target.value = "";
              }}
            />
          </div>
        )}
      </div>

      {error && <ErrorNotice>{error}</ErrorNotice>}

      {/* The list card is hidden when the load failed — the error banner
          above explains it, so a stuck "Loading…" or a misleading "empty"
          never shows. */}
      {!(error && entries === null) && (
      // The whole list area is a drop target for the current folder, so you
      // can drop onto an opened (e.g. spring-loaded) folder's body — not
      // only onto subfolder rows. Subfolder rows stopPropagation, so they
      // win when hovered; the surrounding area resolves to the current dir.
      <div className="card files-list" {...dropProps(cwd)}>
        {visible === null ? (
          <div className="files-empty">{t("common.loading")}</div>
        ) : visible.length === 0 ? (
          <div className="files-empty">
            {hiddenCount > 0 ? t("files.emptyHiddenOnly") : t("files.empty")}
          </div>
        ) : (
          <table className="files-table">
            <thead>
              <tr>
                <th>{t("files.colName")}</th>
                <th className="files-col-size">{t("files.colSize")}</th>
                <th className="files-col-actions" aria-label={t("common.colActions")} />
              </tr>
            </thead>
            <tbody>
              {visible.map((entry) => (
                <tr
                  key={entry.path}
                  draggable={canWrite}
                  onDragStart={(e) => {
                    setDragSrc(entry);
                    e.dataTransfer.effectAllowed = "move";
                    e.dataTransfer.setData("text/plain", entry.path);
                  }}
                  onDragEnd={() => {
                    setDragSrc(null);
                    setDropTarget(null);
                  }}
                  className={dragSrc?.path === entry.path ? "files-row-dragging" : undefined}
                  // Folder rows accept a drop to move the dragged entry into them.
                  {...(entry.is_dir ? dropProps(entry.path) : {})}
                >
                  <td className="files-name-cell">
                    {entry.is_dir ? (
                      <Button variant="link" className="files-name" onClick={() => setCwd(entry.path)}>
                        <Folder size={ICON.md} className="files-icon files-icon-dir" />
                        {entry.name}
                      </Button>
                    ) : (
                      <span className="files-name">
                        <FileIcon size={ICON.md} className="files-icon" />
                        {entry.name}
                      </span>
                    )}
                  </td>
                  <td className="files-col-size">
                    {entry.is_dir ? "—" : formatBytes(entry.size)}
                  </td>
                  <td className="files-col-actions">
                    {!entry.is_dir && (
                      <Button
                        variant="ghost"
                        size="icon"
                        title={t("files.download")}
                        onClick={() => onDownload(entry)}
                      >
                        <Download size={ICON.md} />
                      </Button>
                    )}
                    {canWrite && (
                      <>
                        <Button
                          variant="ghost"
                          size="icon"
                          title={t("files.move")}
                          onClick={() => setMovingPick(entry)}
                          disabled={busy}
                        >
                          <FolderInput size={ICON.md} />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          title={t("files.rename")}
                          onClick={() => setRenaming(entry)}
                          disabled={busy}
                        >
                          <Pencil size={ICON.md} />
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="danger"
                          title={t("common.delete")}
                          onClick={() => setDeleting(entry)}
                          disabled={busy}
                        >
                          <Trash2 size={ICON.md} />
                        </Button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      )}

      {usage &&
        (usage.limit > 0 ? (
          <div className="files-quota">
            <div className="files-quota-label">
              {t("files.quota", {
                used: formatBytes(usage.used),
                limit: formatBytes(usage.limit),
              })}
            </div>
            <div className="files-quota-bar">
              <div
                className="files-quota-fill"
                data-danger={quotaPct >= 90 ? "true" : "false"}
                style={{ width: `${quotaPct}%` }}
              />
            </div>
          </div>
        ) : (
          // No hard limit (unlimited plan): still surface how much storage
          // this workspace is using — it's the useful half of "quota".
          <div className="files-quota">
            <div className="files-quota-label">
              {t("files.quotaUnlimited", { used: formatBytes(usage.used) })}
            </div>
          </div>
        ))}

      {creatingFolder && (
        <PromptModal
          title={t("files.newFolderTitle")}
          label={t("files.newFolderLabel")}
          hint={t("files.newFolderHint")}
          confirmLabel={t("files.create")}
          onSubmit={submitNewFolder}
          onCancel={() => setCreatingFolder(false)}
        />
      )}

      {renaming && (
        <PromptModal
          title={t("files.renameTitle")}
          label={t("files.renameLabel")}
          hint={t("files.renameHint")}
          initialValue={renaming.path}
          confirmLabel={t("common.save")}
          onSubmit={(next) => submitRename(renaming, next)}
          onCancel={() => setRenaming(null)}
        />
      )}

      {deleting && (
        <ConfirmModal
          title={t("common.delete")}
          message={t("files.deleteConfirm", { name: deleting.name })}
          confirmLabel={t("common.delete")}
          danger
          onConfirm={() => confirmDelete(deleting)}
          onCancel={() => setDeleting(null)}
        />
      )}

      {movingPick && (
        <MoveModal
          entry={movingPick}
          token={token!}
          tenant={activeTenant}
          workspace={activeWorkspace}
          onMoved={() => {
            setMovingPick(null);
            refresh();
          }}
          onCancel={() => setMovingPick(null)}
        />
      )}
    </div>
  );
}
