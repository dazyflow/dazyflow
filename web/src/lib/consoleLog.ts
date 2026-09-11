// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// A step's console output, rendered for the terminal the inspector shows.
//
// The Code step records every console call as a row — the line of script it
// came from, the method the author called, and what it said — and sends the
// same three live on the run stream. Both go through here, so a line reads the
// same while the run is going as it does once the run is over and the rows are
// all that is left.

export type LogRow = {
  index?: number;
  line?: number;
  level?: string;
  message?: string;
};

const DIM = "\x1b[2m";
const RESET = "\x1b[0m";

// Colour carries the level, so a reader finds the one error in two hundred
// lines by looking rather than by reading. `log` has none: it is most of what
// a script prints, and colouring the common case leaves nothing to stand out
// against.
const LEVEL_COLOR: Record<string, string> = {
  error: "\x1b[31m",
  warn: "\x1b[33m",
  info: "\x1b[36m",
  debug: DIM,
};

const GUTTER = 3;
const TAG = 5; // "error", the longest level

// consoleLine renders one console call: which line of the script printed it,
// how loudly, and what it said. A line with no level — a shell step's stdout,
// anything that predates this shape — passes through untouched, its own ANSI
// included, because the sender already decided how it should look.
export function consoleLine(
  message: string,
  level?: string,
  scriptLine?: number,
): string {
  if (!level) return message;
  // No line number when the sandbox itself speaks (the truncation notice):
  // no line of the author's produced it, and "0" would claim one did.
  const gutter =
    scriptLine && scriptLine > 0
      ? String(scriptLine).padStart(GUTTER)
      : " ".repeat(GUTTER);
  const tag = level === "log" ? " ".repeat(TAG) : level.padEnd(TAG);
  const color = LEVEL_COLOR[level];
  return (
    `${DIM}${gutter} │${RESET} ` +
    (color ? `${color}${tag}${RESET}` : tag) +
    ` ${message}`
  );
}

// consoleLines renders what a finished step recorded on its logs port. The
// value is data from a run and a script can put anything on a port, so a row
// that is not a log row is skipped rather than drawn as "undefined".
export function consoleLines(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  const lines: string[] = [];
  for (const row of value) {
    if (!row || typeof row !== "object" || Array.isArray(row)) continue;
    const { message, level, line } = row as LogRow;
    if (typeof message !== "string") continue;
    lines.push(
      consoleLine(
        message,
        typeof level === "string" ? level : undefined,
        typeof line === "number" ? line : undefined,
      ),
    );
  }
  return lines;
}
