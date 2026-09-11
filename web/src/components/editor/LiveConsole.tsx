// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";

type Props = {
  lines: string[];
};

// LiveConsole wraps an xterm.js Terminal. xterm renders ANSI escape
// codes (colors, cursor moves, progress bars) and provides scrollback
// + copy/paste — none of which a plain <pre> can do.
export function LiveConsole({ lines }: Props) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const fitRef = useRef<FitAddon | null>(null);
  const lastWrittenRef = useRef<number>(0);

  // Create the terminal once. The dependency array is empty on purpose
  // — re-creating on every prop change would flush the scrollback.
  useEffect(() => {
    if (!containerRef.current) return;
    const term = new Terminal({
      fontFamily: "var(--font-mono), Menlo, monospace",
      // xterm.js needs a real number — it draws on a canvas and can't
      // resolve CSS variables. Keep in sync with --text-xs (12px).
      fontSize: 12,
      lineHeight: 1.2,
      convertEol: true,
      scrollback: 5000,
      theme: {
        background: "#0a0918",
        foreground: "#d8d4ec",
        cursor: "#9f83fe",
        selectionBackground: "rgba(159, 131, 254, 0.3)",
        // A step's errors and warnings arrive as ANSI red and yellow (see
        // consoleLog.ts). xterm's defaults for those are a muddy red and an
        // acid yellow on this near-black violet; these are the same colours a
        // failed and a warning status wear everywhere else in the app, so one
        // red means one thing. Literal values because xterm draws on a canvas
        // and cannot read a CSS variable — keep them in step with theme.css.
        red: "#ff6464", // --status-failed
        brightRed: "#ff6464",
        yellow: "#f5a623", // --status-warning
        brightYellow: "#f5a623",
        cyan: "#9f83fe", // --accent, for console.info
        brightCyan: "#9f83fe",
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
        /* ignore — terminal may be unmounted */
      }
    });
    ro.observe(containerRef.current);

    return () => {
      ro.disconnect();
      term.dispose();
      termRef.current = null;
      fitRef.current = null;
      lastWrittenRef.current = 0;
    };
  }, []);

  // Sync new lines. If the array shrank (run cleared) reset the
  // terminal; otherwise write only the new tail.
  useEffect(() => {
    const term = termRef.current;
    if (!term) return;
    if (lines.length < lastWrittenRef.current) {
      term.clear();
      lastWrittenRef.current = 0;
    }
    for (let i = lastWrittenRef.current; i < lines.length; i++) {
      term.writeln(lines[i]);
    }
    lastWrittenRef.current = lines.length;
  }, [lines]);

  return <div ref={containerRef} className="live-console" />;
}
