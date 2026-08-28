// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";
import { AlertCircle, Check, Upload as UploadIcon, X } from "lucide-react";
import { api } from "./api";
import { Button } from "./components/ui/Button";
import i18n from "./i18n/index";
import { explainApiError } from "./lib/explainApiError";
import { ICON } from "./icons";

// Uploads lifts file-upload state above the router so an upload keeps
// running — and keeps showing progress — while the user navigates between
// pages. A global indicator (bottom-right) shows every active/recent
// upload. A beforeunload guard warns if a reload/close would interrupt one.
// (A full reload still drops in-flight uploads — surviving that needs the
// Background Fetch API / a service worker, deliberately out of scope.)

export type UploadStatus = "queued" | "uploading" | "done" | "error" | "canceled";

export interface UploadTask {
  id: string;
  name: string;
  dest: string; // workspace-relative destination
  dir: string; // folder it's uploaded into
  tenant: string;
  workspace: string;
  size: number;
  progress: number; // 0..1
  status: UploadStatus;
  error?: string;
}

interface UploadsCtx {
  tasks: UploadTask[];
  enqueue: (
    files: File[],
    opts: { token: string; tenant: string; workspace: string; dir: string },
  ) => void;
  cancel: (id: string) => void;
  dismiss: (id: string) => void;
  // Bumps on each successful upload; lastDone carries where it landed so a
  // visible Files view can refresh itself when relevant.
  completedNonce: number;
  lastDone: { tenant: string; workspace: string; dir: string } | null;
}

const Ctx = createContext<UploadsCtx | null>(null);

export function useUploads(): UploadsCtx {
  const c = useContext(Ctx);
  if (!c) throw new Error("useUploads must be used within UploadsProvider");
  return c;
}

export function UploadsProvider({ children }: { children: ReactNode }) {
  const [tasks, setTasks] = useState<UploadTask[]>([]);
  const [completedNonce, setCompletedNonce] = useState(0);
  const [lastDone, setLastDone] = useState<UploadsCtx["lastDone"]>(null);

  const idSeq = useRef(0);
  const aborters = useRef(new Map<string, AbortController>());
  // The File + token aren't kept in render state — only in this side map.
  const payloads = useRef(new Map<string, { file: File; token: string }>());
  const running = useRef(false);
  const tasksRef = useRef<UploadTask[]>([]);
  tasksRef.current = tasks;

  const update = useCallback(
    (id: string, patch: Partial<UploadTask>) =>
      setTasks((ts) => ts.map((t) => (t.id === id ? { ...t, ...patch } : t))),
    [],
  );

  const dismiss = useCallback((id: string) => {
    payloads.current.delete(id);
    aborters.current.delete(id);
    setTasks((ts) => ts.filter((t) => t.id !== id));
  }, []);

  // Drives the queue: at most one upload in flight at a time. Re-invoked by
  // an effect whenever tasks change (enqueue, or a task finishing), so it
  // self-pumps to the next queued item.
  const pump = useCallback(() => {
    if (running.current) return;
    const next = tasksRef.current.find((t) => t.status === "queued");
    if (!next) return;
    const payload = payloads.current.get(next.id);
    if (!payload) return;
    running.current = true;
    const ctrl = new AbortController();
    aborters.current.set(next.id, ctrl);
    update(next.id, { status: "uploading", progress: 0 });
    api
      .uploadWorkspaceFileProgress(
        payload.token,
        next.tenant,
        next.workspace,
        payload.file,
        next.dest,
        { onProgress: (f) => update(next.id, { progress: f }), signal: ctrl.signal },
      )
      .then(() => {
        update(next.id, { status: "done", progress: 1 });
        setLastDone({ tenant: next.tenant, workspace: next.workspace, dir: next.dir });
        setCompletedNonce((n) => n + 1);
        window.setTimeout(() => dismiss(next.id), 4000); // auto-clear success
      })
      .catch((e: unknown) => {
        const aborted = e instanceof DOMException && e.name === "AbortError";
        if (aborted) {
          update(next.id, { status: "canceled" });
          window.setTimeout(() => dismiss(next.id), 2500);
        } else {
          // Run through explainApiError so a failed upload shows plain
          // guidance ("the file is too large", "your session expired")
          // instead of a raw Go/HTTP string. The pump callback has no React
          // `t` in scope, so use the i18n singleton's `t` (same instance the
          // UI renders with, so it tracks the active language).
          update(next.id, {
            status: "error",
            error: explainApiError(e, i18n.t),
          });
        }
      })
      .finally(() => {
        aborters.current.delete(next.id);
        payloads.current.delete(next.id);
        running.current = false;
        setTasks((ts) => [...ts]); // nudge the pump effect to start the next
      });
  }, [update, dismiss]);

  useEffect(() => {
    pump();
  }, [tasks, pump]);

  const enqueue = useCallback<UploadsCtx["enqueue"]>((files, opts) => {
    const created = files.map((file) => {
      const id = `up-${idSeq.current++}`;
      const dest = opts.dir ? `${opts.dir}/${file.name}` : file.name;
      payloads.current.set(id, { file, token: opts.token });
      return {
        id,
        name: file.name,
        dest,
        dir: opts.dir,
        tenant: opts.tenant,
        workspace: opts.workspace,
        size: file.size,
        progress: 0,
        status: "queued" as const,
      };
    });
    setTasks((ts) => [...ts, ...created]);
  }, []);

  const cancel = useCallback(
    (id: string) => {
      const ctrl = aborters.current.get(id);
      if (ctrl) ctrl.abort();
      else {
        payloads.current.delete(id);
        update(id, { status: "canceled" });
        window.setTimeout(() => dismiss(id), 2500);
      }
    },
    [update, dismiss],
  );

  // Warn before a reload/close would interrupt an in-flight or queued upload.
  const active = tasks.some((t) => t.status === "uploading" || t.status === "queued");
  useEffect(() => {
    if (!active) return;
    const handler = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = "";
    };
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [active]);

  return (
    <Ctx.Provider value={{ tasks, enqueue, cancel, dismiss, completedNonce, lastDone }}>
      {children}
      <UploadsIndicator />
    </Ctx.Provider>
  );
}

function UploadsIndicator() {
  const { t } = useTranslation();
  const { tasks, cancel, dismiss } = useUploads();
  if (tasks.length === 0) return null;
  const active = tasks.filter((x) => x.status === "uploading" || x.status === "queued").length;
  const anyFailed = tasks.some((x) => x.status === "error");
  const headerText =
    active > 0
      ? t("uploads.uploading", { count: active })
      : anyFailed
        ? t("uploads.someFailed")
        : t("uploads.done");
  return (
    <div className="uploads-panel" role="status" aria-live="polite">
      <div className="uploads-head" data-failed={active === 0 && anyFailed ? "true" : undefined}>
        <UploadIcon size={ICON.sm} />
        <span>{headerText}</span>
      </div>
      <div className="uploads-list">
        {tasks.map((task) => (
          <div key={task.id} className="upload-row" data-status={task.status}>
            <div className="upload-row-top">
              <span className="upload-status-icon">
                {task.status === "done" ? (
                  <Check size={ICON.sm} />
                ) : task.status === "error" ? (
                  <AlertCircle size={ICON.sm} />
                ) : (
                  <UploadIcon size={ICON.sm} />
                )}
              </span>
              <span className="upload-name" title={task.dest}>
                {task.name}
              </span>
              {(task.status === "uploading" || task.status === "queued") && (
                <Button
                  variant="ghost"
                  size="icon"
                  className="upload-x"
                  title={t("uploads.cancel")}
                  onClick={() => cancel(task.id)}
                >
                  <X size={ICON.sm} />
                </Button>
              )}
              {(task.status === "done" || task.status === "error" || task.status === "canceled") && (
                <Button
                  variant="ghost"
                  size="icon"
                  className="upload-x"
                  title={t("common.dismiss")}
                  onClick={() => dismiss(task.id)}
                >
                  <X size={ICON.sm} />
                </Button>
              )}
            </div>
            <div className="upload-row-meta">
              {task.status === "uploading"
                ? `${Math.round(task.progress * 100)}%`
                : task.status === "queued"
                  ? t("uploads.queued")
                  : task.status === "done"
                    ? t("uploads.complete")
                    : task.status === "canceled"
                      ? t("uploads.canceled")
                      : (task.error ?? t("uploads.failed"))}
            </div>
            <div className="upload-bar">
              <div
                className="upload-bar-fill"
                style={{
                  width: `${(task.status === "done" ? 1 : task.progress) * 100}%`,
                }}
              />
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
