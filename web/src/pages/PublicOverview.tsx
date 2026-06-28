// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import { useParams } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { Maximize, Minimize } from "lucide-react";
import { api, isErrorCode } from "../api";
import { Button } from "../components/Button";
import { FlowIcon } from "../icons";
import { formatRelative } from "../lib/datetime";
import { formatNextRun } from "../lib/schedule";
import type { PublicOverview as PublicOverviewData } from "../types";

// PublicOverview is the login-free, full-screen workspace status board behind
// a share link — designed to live on a wall-mounted TV. It polls the public
// snapshot endpoint (the share token in the URL is the only credential) and
// re-renders the counters + per-flow grid as runs come and go. No AppShell,
// no navigation: it's a single self-contained surface.
const POLL_MS = 5000; // live enough for a wall, gentle on the daemon

export function PublicOverview() {
  const { token = "" } = useParams();
  const { t } = useTranslation();
  const [data, setData] = useState<PublicOverviewData | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [stale, setStale] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const [fullscreen, setFullscreen] = useState(false);
  // Tracks whether the very first fetch has resolved, so we show a spinner
  // rather than an empty board on initial load.
  const loadedRef = useRef(false);

  const poll = useCallback(async () => {
    try {
      const d = await api.getPublicOverview(token);
      setData(d);
      setStale(false);
      setNotFound(false);
    } catch (e) {
      if (isErrorCode(e, "share_not_found")) {
        setNotFound(true);
      } else {
        // Transient failure (network blip): keep the last good board on
        // screen but flag it stale so the wall doesn't silently freeze.
        setStale(true);
      }
    } finally {
      loadedRef.current = true;
    }
  }, [token]);

  useEffect(() => {
    poll();
    const id = window.setInterval(poll, POLL_MS);
    return () => window.clearInterval(id);
  }, [poll]);

  // A ticking clock + "updated Ns ago" need a steady re-render even between polls.
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, []);

  useEffect(() => {
    const onChange = () => setFullscreen(!!document.fullscreenElement);
    document.addEventListener("fullscreenchange", onChange);
    return () => document.removeEventListener("fullscreenchange", onChange);
  }, []);

  const toggleFullscreen = useCallback(() => {
    if (document.fullscreenElement) {
      document.exitFullscreen().catch(() => {});
    } else {
      document.documentElement.requestFullscreen().catch(() => {});
    }
  }, []);

  if (notFound) {
    return (
      <div className="tv-view tv-message">
        <div>
          <h1>{t("tv.notFoundTitle")}</h1>
          <p>{t("tv.notFoundBody")}</p>
        </div>
      </div>
    );
  }

  if (!data && !loadedRef.current) {
    return (
      <div className="tv-view tv-message">
        <p>{t("common.loading")}</p>
      </div>
    );
  }

  const stats = data?.stats;
  const flows = data?.flows ?? [];
  const health = boardHealth(data);

  // 24-hour clock (e.g. 14:05) — no AM/PM, regardless of system locale.
  const clock = new Date(now).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });

  return (
    <div className={"tv-view tv-health-" + health}>
      <header className="tv-head">
        <div className="tv-head-left">
          {data?.icon && (
            <span className="tv-org-ico">
              <FlowIcon icon={data.icon} size={48} />
            </span>
          )}
          <div className="tv-titles">
            {data?.label ? (
              <>
                <span className="tv-eyebrow">{t("tv.title")}</span>
                <h1 className="tv-title">{data.label}</h1>
              </>
            ) : (
              <h1 className="tv-title">{t("tv.title")}</h1>
            )}
          </div>
        </div>
        <div className="tv-head-right">
          <span className="tv-clock">{clock}</span>
          <span className={"tv-pulse " + (stale ? "stale" : "live")}>
            <span className="status-dot running" />
            {stale ? t("tv.reconnecting") : t("tv.live")}
          </span>
          <Button
            className="tv-fs"
            onClick={toggleFullscreen}
            title={t("tv.fullscreen")}
          >
            {fullscreen ? <Minimize size={20} /> : <Maximize size={20} />}
          </Button>
        </div>
      </header>

      <div className="tv-stats">
        <TvStat
          label={t("dashboard.runsToday")}
          value={stats ? String(stats.runs_today) : "—"}
        />
        <TvStat
          label={t("dashboard.successRate")}
          value={
            stats && stats.success_rate != null
              ? `${stats.success_rate}%`
              : "—"
          }
          tone={
            stats && stats.success_rate != null && stats.success_rate < 80
              ? "warn"
              : "good"
          }
        />
        <TvStat
          label={t("dashboard.needsAttention")}
          value={stats ? String(stats.failed) : "—"}
          tone={stats && stats.failed > 0 ? "bad" : "good"}
        />
        <TvStat
          label={t("tv.running")}
          value={stats ? String(stats.running) : "—"}
          tone={stats && stats.running > 0 ? "live" : "neutral"}
        />
      </div>

      {flows.length === 0 ? (
        <div className="tv-empty">{t("tv.noFlows")}</div>
      ) : (
        <div className="tv-grid">
          {flows.map((f, i) => (
            <div
              key={f.name + i}
              className={"tv-flow tv-flow-" + (f.last_status || "none")}
            >
              <div className="tv-flow-top">
                <span className="tv-flow-ico">
                  <FlowIcon icon={f.icon} size={24} />
                </span>
                <span className="tv-flow-name">{f.name}</span>
              </div>
              <div className="tv-flow-bottom">
                <span className={"tv-pill tv-pill-" + (f.last_status || "none")}>
                  <span
                    className={"status-dot " + (f.last_status || "queued")}
                  />
                  {f.last_status
                    ? t("tv.status." + f.last_status, {
                        defaultValue: f.last_status,
                      })
                    : t("tv.neverRun")}
                </span>
                <div className="tv-flow-meta">
                  {f.history && f.history.length > 0 && (
                    <span
                      className="tv-spark"
                      role="img"
                      aria-label={t("tv.historyLabel", {
                        count: f.history.length,
                      })}
                      title={t("tv.historyLabel", { count: f.history.length })}
                    >
                      {/* newest-first from the API; render oldest→newest so the
                          most recent run sits on the right, like a timeline. */}
                      {[...f.history].reverse().map((s, j) => (
                        <span key={j} className={"tv-spark-bar " + s} />
                      ))}
                    </span>
                  )}
                  {f.last_run_at && (
                    <span className="tv-flow-time">
                      {formatRelative(f.last_run_at, t)}
                    </span>
                  )}
                  {f.next_run_at && (
                    <span
                      className="tv-flow-next"
                      title={t("tv.nextRun", {
                        when: formatNextRun(f.next_run_at, t),
                      })}
                    >
                      {t("tv.nextRun", {
                        when: formatNextRun(f.next_run_at, t),
                      })}
                    </span>
                  )}
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      <footer className="tv-foot">
        <span>
          {t("dashboard.flowSummary", {
            total: stats?.total_flows ?? flows.length,
            live: stats?.live_flows ?? 0,
          })}
        </span>
        {data && (
          <span className="tv-updated">
            {t("tv.updated", { when: formatRelative(data.generated_at, t) })}
          </span>
        )}
      </footer>
    </div>
  );
}

function TvStat({
  label,
  value,
  tone = "neutral",
}: {
  label: string;
  value: string;
  tone?: "neutral" | "good" | "warn" | "bad" | "live";
}) {
  return (
    <div className={"tv-stat tv-stat-" + tone}>
      <span className="tv-stat-value">{value}</span>
      <span className="tv-stat-label">{label}</span>
    </div>
  );
}

// boardHealth is the single colour the whole board leans toward: red when
// anything has failed, blue while work is in flight, green when all clear.
// Drives the ambient accent so a glance from across the room reads right.
function boardHealth(d: PublicOverviewData | null): "bad" | "busy" | "good" {
  if (!d) return "good";
  if (d.stats.failed > 0) return "bad";
  if (d.stats.running > 0) return "busy";
  return "good";
}
