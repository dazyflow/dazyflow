// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef } from "react";

// Escape dismisses a dialog.
//
// Every modal in the app already closes on a backdrop click, because that falls
// out of the markup — the backdrop is the element you attach onClick to. Escape
// does not fall out of anything: it needs a listener, so it was present in nine
// dialogs and missing from twelve. A user who learns Escape works on the
// delete-flow confirm found it dead on the MCP client wizard, which is worse
// than if it had never worked anywhere.
//
// This is the exact listener those nine had, written once. Call it in any
// component that renders a backdrop; scripts/check-modal-a11y.mjs fails the
// build if one doesn't.
//
// `window`, not the dialog element: a dialog rendered through a portal may not
// have focus inside it yet (nothing autofocused, or the user clicked the
// backdrop), and a key handler bound to an unfocused subtree never fires.
// The callback is held in a ref so the listener registers ONCE per mount rather
// than on every render. Without that, a call site passing an inline arrow — which
// several need, to gate dismissal on "not mid-save" — would add and remove a
// listener on each render for no reason.
export function useEscapeToClose(onClose: () => void) {
  const ref = useRef(onClose);
  ref.current = onClose;
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") ref.current();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
}
