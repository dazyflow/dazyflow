import { jsx as _jsx } from "react/jsx-runtime";
import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
// LiveConsole wraps an xterm.js Terminal. xterm renders ANSI escape
// codes (colors, cursor moves, progress bars) and provides scrollback
// + copy/paste — none of which a plain <pre> can do.
export function LiveConsole({ lines }) {
    const containerRef = useRef(null);
    const termRef = useRef(null);
    const fitRef = useRef(null);
    const lastWrittenRef = useRef(0);
    // Create the terminal once. The dependency array is empty on purpose
    // — re-creating on every prop change would flush the scrollback.
    useEffect(() => {
        if (!containerRef.current)
            return;
        const term = new Terminal({
            fontFamily: "var(--font-mono), Menlo, monospace",
            fontSize: 12,
            lineHeight: 1.2,
            convertEol: true,
            scrollback: 5000,
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
            }
            catch {
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
        if (!term)
            return;
        if (lines.length < lastWrittenRef.current) {
            term.clear();
            lastWrittenRef.current = 0;
        }
        for (let i = lastWrittenRef.current; i < lines.length; i++) {
            term.writeln(lines[i]);
        }
        lastWrittenRef.current = lines.length;
    }, [lines]);
    return _jsx("div", { ref: containerRef, className: "live-console" });
}
