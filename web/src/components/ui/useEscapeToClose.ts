// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef } from "react";

// Escape dismisses a dialog.
//
// Closing on a backdrop click falls out of the markup; Escape needs a listener,
// so it was present in nine dialogs and missing from twelve. A user who learns
// Escape works on the delete-flow confirm found it dead on the MCP client
// wizard, which is worse than if it had never worked anywhere. This is that
// listener written once — scripts/check-modal-a11y.mjs fails the build if a
// component rendering a backdrop doesn't call it.
//
// Bound to `window`, not the dialog element: a portaled dialog may not have
// focus inside it yet, and a key handler on an unfocused subtree never fires.
// The callback is held in a ref so the listener registers ONCE per mount — call
// sites pass inline arrows to gate dismissal on "not mid-save".
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
