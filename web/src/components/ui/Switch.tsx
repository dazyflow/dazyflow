// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { ReactNode } from "react";

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
  label?: ReactNode;
  description?: ReactNode;
  disabled?: boolean;
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
