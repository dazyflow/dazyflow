// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import {
  ScrollText,
  Search,
  Pause,
  Play,
  Trash2,
  AlertCircle,
} from "lucide-react";
import { Trans, useTranslation } from "react-i18next";
import { useAuth } from "../../auth";
import { api, APIError } from "../../api";
import { Button } from "../../components/ui/Button";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { ICON } from "../../icons";

// Keep at most this many lines in memory. The terminal's own scrollback is
// separate; this bounds the buffer we re-render from when the filter changes.
const MAX_BUFFER = 5000;

export function AdminSystemLog() {
  const { t } = useTranslation();
  const { token, hasPerm } = useAuth();

  const containerRef = useRef<HTMLDivElement | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const linesRef = useRef<string[]>([]);
  const regexRef = useRef<RegExp | null>(null);
  const followRef = useRef(true);

  const [filter, setFilter] = useState("");
  const [filterError, setFilterError] = useState<string | null>(null);
  const [following, setFollowing] = useState(true);
  const [connected, setConnected] = useState(false);
  const [streamError, setStreamError] = useState<string | null>(null);
  const [count, setCount] = useState(0);

  const isAdmin = hasPerm("platform:admin");

  const rerender = useCallback(() => {
    const term = termRef.current;
    if (!term) return;
    term.clear();
    const re = regexRef.current;
    for (const line of linesRef.current) {
      if (!re || re.test(line)) term.writeln(line);
    }
  }, []);

  // Create the terminal once. Empty deps on purpose — recreating would
  // flush scrollback. Mirrors the LiveConsole component's setup.
  useEffect(() => {
    if (!containerRef.current) return;
    const term = new Terminal({
      fontFamily: "var(--font-mono), Menlo, monospace",
      // A real number, not a token: xterm draws on a canvas and cannot
      // resolve CSS variables. Keep in sync with --text-xs (12px).
      fontSize: 12,
      lineHeight: 1.2,
      convertEol: true,
      scrollback: MAX_BUFFER,
      disableStdin: true,
      theme: {
        background: "#0a0918",
        foreground: "#d8d4ec",
        cursor: "#9f83fe",
        selectionBackground: "rgba(159, 131, 254, 0.3)",
      },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(containerRef.current);
    fit.fit();
    termRef.current = term;
    fitRef.current = fit;

    const ro = new ResizeObserver(() => {
      try {
        fit.fit();
      } catch {
        /* ignore — terminal may be unmounted mid-resize */
      }
    });
    ro.observe(containerRef.current);

    return () => {
      ro.disconnect();
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
    };
  }, []);

  useEffect(() => {
    if (!token || !isAdmin) return;
    let stopped = false;
    const ctrl = new AbortController();
    let retry: ReturnType<typeof setTimeout> | undefined;

    const onLine = (line: string) => {
      const buf = linesRef.current;
      buf.push(line);
      if (buf.length > MAX_BUFFER) buf.splice(0, buf.length - MAX_BUFFER);
      setCount(buf.length);
      const term = termRef.current;
      if (!term || !followRef.current) return;
      const re = regexRef.current;
      if (!re || re.test(line)) term.writeln(line);
    };

    const run = () => {
      api
        .streamSystemLog(token, onLine, ctrl.signal)
        .then(() => {
          if (!stopped) {
            setConnected(false);
            retry = setTimeout(run, 1500);
          }
        })
        .catch((e) => {
          if (stopped || ctrl.signal.aborted) return;
          setConnected(false);
          const err = e as APIError | Error;
          if (err instanceof APIError && err.status === 501) {
            setStreamError(t("admin.systemLog.notEnabled"));
            return; // not retryable
          }
          if (err instanceof APIError && err.status === 403) {
            setStreamError(t("admin.systemLog.needPlatformAdmin"));
            return;
          }
          setStreamError(err.message);
          retry = setTimeout(run, 1500);
        });
      setConnected(true);
      setStreamError(null);
    };
    run();

    return () => {
      stopped = true;
      if (retry) clearTimeout(retry);
      ctrl.abort();
    };
  }, [token, isAdmin, t]);

  useEffect(() => {
    const trimmed = filter.trim();
    if (!trimmed) {
      regexRef.current = null;
      setFilterError(null);
      rerender();
      return;
    }
    try {
      regexRef.current = new RegExp(trimmed, "i");
      setFilterError(null);
      rerender();
    } catch (e) {
      setFilterError((e as Error).message);
    }
  }, [filter, rerender]);

  const toggleFollow = () => {
    setFollowing((f) => {
      const next = !f;
      followRef.current = next;
      if (next) rerender();
      return next;
    });
  };

  const clear = () => {
    linesRef.current = [];
    setCount(0);
    termRef.current?.clear();
  };

  if (!isAdmin) {
    return (
      <ErrorNotice>
        <Trans i18nKey="admin.systemLog.needPlatformAdmin" components={[<code />]} />
      </ErrorNotice>
    );
  }

  return (
    <div className="system-log">
      <div className="page-title">
        <div>
          <h1>
            <ScrollText size={ICON.xl} />
            {t("admin.systemLog.title")}
          </h1>
          <div className="sub">{t("admin.systemLog.subtitle")}</div>
        </div>
      </div>

      <div className="system-log-toolbar">
        <div className="system-log-search">
          <Search size={ICON.sm} className="system-log-search-icon" />
          <input
            type="text"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder={t("admin.systemLog.filterPlaceholder")}
            spellCheck={false}
            autoCapitalize="off"
            autoCorrect="off"
            aria-label={t("admin.systemLog.filterAria")}
            aria-invalid={!!filterError}
          />
        </div>
        <Button
          variant="ghost"
          size="sm"
          icon={following ? <Pause size={ICON.sm} /> : <Play size={ICON.sm} />}
          onClick={toggleFollow}
          aria-pressed={following}
          title={t("admin.systemLog.followHint")}
        >
          {following ? t("admin.systemLog.pause") : t("admin.systemLog.follow")}
        </Button>
        <Button
          variant="ghost"
          size="sm"
          icon={<Trash2 size={ICON.sm} />}
          onClick={clear}
          title={t("admin.systemLog.clearHint")}
        >
          {t("admin.systemLog.clear")}
        </Button>
        <span className={`system-log-status ${connected ? "is-live" : "is-down"}`}>
          <span className="system-log-dot" />
          {connected ? t("admin.systemLog.live") : t("admin.systemLog.reconnecting")}
        </span>
        <span className="desc system-log-count">
          {t("admin.systemLog.lineCount", { count })}
        </span>
      </div>

      {filterError && (
        <div className="system-log-hint is-error">
          <AlertCircle className="icon-lede" size={ICON.sm} />
          {t("admin.systemLog.badRegex", { error: filterError })}
        </div>
      )}
      {streamError && !filterError && (
        <div className="system-log-hint is-error">
          <AlertCircle className="icon-lede" size={ICON.sm} />
          {streamError}
        </div>
      )}

      <div ref={containerRef} className="system-log-term" />
    </div>
  );
}
