// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { Check } from "lucide-react";
import { ICON } from "../../../icons";

// Says so when a run worked. Without this the only success signal was a border
// tint on each node, which reads as "nothing happened" to anyone not looking
// for it.
//
// It lives in the TOOLBAR, beside the button that started the run, and it fades
// on its own. It used to float over the canvas at top-centre and stay until
// dismissed, which is the worst of both: a flow reads from its top-left, so
// that is exactly where the panel sat, and it sat there until the next run or
// an explicit click. The outcome now appears where the user was already
// looking — they just pressed Run — and then gets out of the way.
//
// What the last step PRODUCED is no longer printed here. It used to fold open
// into a `JSON.stringify(…, null, 2)` scroll box; for the audience this product
// is for that is a wall of syntax rather than an answer, and the canvas already
// answers it better in two places — the data face folds every card open to show
// what each step emitted, and an output pin peeks its value on hover. "See the
// full run" remains for the whole picture.
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

  // The full sentence, naming the step whose output was found. It is the title
  // rather than the visible text because the toolbar is the one place with no
  // room to spare — the visible half truncates, and this is what a hover (or a
  // screen reader, via the status role) gets in full.
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
