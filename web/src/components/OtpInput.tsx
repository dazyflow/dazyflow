// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  useEffect,
  useRef,
  useState,
  type ClipboardEvent,
  type KeyboardEvent,
} from "react";

// OtpInput renders a row of six grouped single-digit boxes for TOTP code
// entry — the operator-app norm (GitHub, 1Password, …): typing advances,
// Backspace retreats, ←/→ navigate, and a paste anywhere distributes the
// digits across the boxes. It's a controlled component: `value` is the
// (≤6-char) code string and `onChange` emits the new one; clearing `value`
// to "" from the parent resets the boxes. `onComplete` fires once all six
// boxes hold a digit, so the caller can auto-submit.
const LEN = 6;

function toBoxes(v: string): string[] {
  const digits = (v || "").replace(/\D/g, "").slice(0, LEN).split("");
  return Array.from({ length: LEN }, (_, i) => digits[i] ?? "");
}

export function OtpInput({
  value,
  onChange,
  onComplete,
  disabled = false,
  autoFocus = false,
  ariaLabel = "Digit",
}: {
  value: string;
  onChange: (v: string) => void;
  onComplete?: (v: string) => void;
  disabled?: boolean;
  autoFocus?: boolean;
  ariaLabel?: string;
}) {
  const [boxes, setBoxes] = useState<string[]>(() => toBoxes(value));
  const refs = useRef<Array<HTMLInputElement | null>>([]);
  // Mirror the latest boxes in a ref so event handlers never read a stale
  // closure — matters when several input events land before React
  // re-renders (paste, IME, autofill, fast typing).
  const boxesRef = useRef(boxes);
  boxesRef.current = boxes;

  // Mirror an external reset (e.g. the parent clears the field after a
  // failed verify) into the boxes without clobbering local edits.
  useEffect(() => {
    if (value !== boxesRef.current.join("")) setBoxes(toBoxes(value));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value]);

  const focusBox = (i: number) => {
    const el = refs.current[Math.max(0, Math.min(LEN - 1, i))];
    el?.focus();
    el?.select();
  };

  const emit = (next: string[]) => {
    boxesRef.current = next;
    setBoxes(next);
    const v = next.join("");
    onChange(v);
    if (v.length === LEN && next.every(Boolean)) onComplete?.(v);
  };

  // Distribute a (possibly pasted/autofilled) string across the boxes
  // starting at `start`, stripping non-digits, then park the caret on the
  // last filled box.
  const fillFrom = (start: number, raw: string) => {
    const digits = raw.replace(/\D/g, "").slice(0, LEN - start);
    if (!digits) return;
    const next = boxesRef.current.slice();
    for (let k = 0; k < digits.length; k++) next[start + k] = digits[k];
    emit(next);
    focusBox(Math.min(start + digits.length, LEN - 1));
  };

  const handleChange = (i: number, raw: string) => {
    if (raw.length > 1) {
      fillFrom(i, raw);
      return;
    }
    if (raw && !/^\d$/.test(raw)) return;
    const next = boxesRef.current.slice();
    next[i] = raw;
    emit(next);
    if (raw && i < LEN - 1) focusBox(i + 1);
  };

  const handleKeyDown = (i: number, e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Backspace" && !boxesRef.current[i] && i > 0) {
      e.preventDefault();
      const next = boxesRef.current.slice();
      next[i - 1] = "";
      emit(next);
      focusBox(i - 1);
    } else if (e.key === "ArrowLeft" && i > 0) {
      e.preventDefault();
      focusBox(i - 1);
    } else if (e.key === "ArrowRight" && i < LEN - 1) {
      e.preventDefault();
      focusBox(i + 1);
    }
  };

  const handlePaste = (i: number, e: ClipboardEvent<HTMLInputElement>) => {
    const data = e.clipboardData?.getData("text") ?? "";
    if (!data) return;
    e.preventDefault();
    fillFrom(i, data);
  };

  return (
    <div className="otp-row">
      {boxes.map((d, i) => (
        <input
          key={i}
          ref={(el) => {
            refs.current[i] = el;
          }}
          className="otp-box"
          type="text"
          inputMode="numeric"
          pattern="[0-9]*"
          maxLength={1}
          value={d}
          disabled={disabled}
          autoFocus={autoFocus && i === 0}
          autoComplete={i === 0 ? "one-time-code" : "off"}
          aria-label={`${ariaLabel} ${i + 1}`}
          onChange={(e) => handleChange(i, e.target.value)}
          onKeyDown={(e) => handleKeyDown(i, e)}
          onPaste={(e) => handlePaste(i, e)}
          onFocus={(e) => e.target.select()}
        />
      ))}
    </div>
  );
}
