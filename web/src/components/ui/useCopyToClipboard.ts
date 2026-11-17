// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import { FEEDBACK } from "../../lib/timing";

// useCopyToClipboard writes text to the clipboard and raises a "copied" flag
// for FEEDBACK.copied — but only once the write has actually succeeded. When
// the clipboard is blocked (non-secure context, denied permission) the flag
// stays down and the visible text remains there for a manual copy.
//
// `key` lets one surface with several copy buttons tell them apart:
// copy(secret, "secret") raises copiedKey === "secret" and leaves the others
// alone. `copied` is true while any key is showing.
export function useCopyToClipboard<K extends string = string>() {
  const [copiedKey, setCopiedKey] = useState<K | "" | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  const copy = useCallback(async (text: string, key?: K): Promise<boolean> => {
    try {
      if (!navigator.clipboard) return false;
      await navigator.clipboard.writeText(text);
    } catch {
      return false;
    }
    setCopiedKey(key ?? "");
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopiedKey(null), FEEDBACK.copied);
    return true;
  }, []);

  return { copied: copiedKey !== null, copiedKey, copy };
}
