// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Remembers the JSON payload last used in the editor's "Send test event"
// dialog, per flow.
//
// Without this the dialog regenerated a fresh synthetic sample every time it
// opened, so a payload you had shaped to reproduce something — the awkward
// field, the empty array, the real body you pasted out of a provider's docs —
// lasted exactly one firing and had to be typed again for the next. Testing an
// edge case twice meant preparing it twice.
//
// localStorage, keyed by flow, so it is per person and per browser. That is
// the right scope for a scratch payload: it is not part of the flow, it is not
// something a teammate should inherit, and it must not ride along in a publish
// or show up in the draft-vs-published diff. It follows the same convention as
// the sticky last-run id this editor already keeps (dazyflow.lastRun.<id>).

const KEY_PREFIX = "dazyflow.testEvent.";

// PERSIST_CAP bounds what we are willing to keep. The /test-trigger endpoint
// accepts up to 1 MiB, but localStorage is a ~5 MB budget shared with
// everything else this app stores, and evicting the rest of it to remember one
// enormous pasted body is a bad trade. Above the cap the payload still fires —
// it just isn't remembered.
const PERSIST_CAP = 256 * 1024;

function keyFor(flowID: string | undefined): string | null {
  const id = flowID?.trim();
  return id ? KEY_PREFIX + id : null;
}

// loadTestEvent returns the saved payload for a flow, or null when there is
// none (or when storage can't be read at all — a private window, a browser set
// to block site data). Callers fall back to a generated sample.
export function loadTestEvent(flowID: string | undefined): string | null {
  const key = keyFor(flowID);
  if (!key) return null;
  try {
    const saved = localStorage.getItem(key);
    // An empty string is a payload someone cleared out, not one to restore:
    // handing the dialog "" would look like the feature lost their edit.
    return saved && saved.trim() !== "" ? saved : null;
  } catch {
    return null;
  }
}

// saveTestEvent remembers a payload for next time. Best-effort by contract —
// every failure path (no flow id yet on an unsaved flow, storage disabled, the
// quota exhausted, an oversized body) leaves the dialog working and simply
// doesn't remember. Nothing here is allowed to interrupt firing a test.
export function saveTestEvent(flowID: string | undefined, json: string): void {
  const key = keyFor(flowID);
  if (!key) return;
  if (json.trim() === "") {
    clearTestEvent(flowID);
    return;
  }
  if (json.length > PERSIST_CAP) return;
  try {
    localStorage.setItem(key, json);
  } catch {
    /* private mode or quota — remembering the payload is a convenience */
  }
}

// clearTestEvent forgets the saved payload, so the dialog goes back to
// generating a sample. Backs the dialog's "Reset to sample": a saved payload
// otherwise outlives the shape it was written for, and a form whose fields
// changed would keep offering the old body forever.
export function clearTestEvent(flowID: string | undefined): void {
  const key = keyFor(flowID);
  if (!key) return;
  try {
    localStorage.removeItem(key);
  } catch {
    /* nothing to do — it was never stored */
  }
}
