// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { Check } from "lucide-react";
import { ICON } from "../../../icons";

const FADE_AFTER_MS = 6000;

export function RunSucceededStatus({
  run,
  onDismiss,
}: {
  run: { runID: string; label: string };
  onDismiss: () => void;
}) {
  const { t } = useTranslation();
  // Held while the pointer or the keyboard is on it, so "See the full run"
  // cannot disappear from under the cursor half a click into reaching for it.
  const [held, setHeld] = useState(false);

  // Through a ref because the caller passes a fresh arrow (`() => setRunDone(
  // null)`) on every render: as an effect dependency that would restart the
  // timer on each one, and a busy editor re-renders often enough that it would
  // never fire. The effect keys on the RUN instead — a new run is a new
  // countdown, and re-renders in between leave it alone.
  const dismissRef = useRef(onDismiss);
  dismissRef.current = onDismiss;

  useEffect(() => {
    if (held) return;
    const timer = setTimeout(() => dismissRef.current(), FADE_AFTER_MS);
    return () => clearTimeout(timer);
  }, [held, run.runID]);

  const sentence = run.label
    ? t("editor.runSucceededWith", { label: run.label })
    : t("editor.runSucceeded");

  return (
    <div
      role="status"
      className="editor-run-status"
      title={sentence}
      onPointerEnter={() => setHeld(true)}
      onPointerLeave={() => setHeld(false)}
      onFocus={() => setHeld(true)}
      onBlur={() => setHeld(false)}
    >
      <Check size={ICON.sm} className="editor-run-status-tick" aria-hidden="true" />
      <span className="editor-run-status-text">{sentence}</span>
      <Link className="editor-run-status-link" to={`/runs/${run.runID}`}>
        {t("editor.runSucceededDetails")}
      </Link>
    </div>
  );
}
