// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from "react";

// Switch is the app's on/off toggle. A real <input type="checkbox"
// role="switch"> stays in the tree — keyboard, forms and screen
// readers behave exactly like the native control — while the visible
// UI is the track + thumb. Use it instead of a bare checkbox wherever
// the value is a setting being turned on or off (a checkbox reads as
// "select this item"; a switch reads as "turn this on"). The optional
// description renders as a muted line under the label, so callers
// don't need a separate .desc block.
export function Switch({
  checked,
  onChange,
  label,
  description,
  disabled,
  compact,
  ariaLabel,
}: {
  checked: boolean;
  onChange: (checked: boolean) => void;
  // label is optional for slots where the name is rendered elsewhere
  // (an inline pin row, a grid cell) — pass ariaLabel then so the
  // control still announces itself.
  label?: ReactNode;
  description?: ReactNode;
  disabled?: boolean;
  // compact shrinks the track for dense surfaces (node pin rows,
  // permission grids).
  compact?: boolean;
  ariaLabel?: string;
}) {
  return (
    <label
      className={
        "dz-switch" + (disabled ? " disabled" : "") + (compact ? " compact" : "")
      }
    >
      <input
        type="checkbox"
        role="switch"
        checked={checked}
        disabled={disabled}
        aria-label={ariaLabel}
        onChange={(e) => onChange(e.target.checked)}
      />
      <span className="dz-switch-track" aria-hidden="true">
        <span className="dz-switch-thumb" />
      </span>
      {(label || description) && (
        <span className="dz-switch-text">
          {label && <span className="dz-switch-label">{label}</span>}
          {description && <span className="dz-switch-desc">{description}</span>}
        </span>
      )}
    </label>
  );
}
