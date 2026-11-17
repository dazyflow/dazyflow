// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import { FEEDBACK } from "../../lib/timing";

// useCopyToClipboard writes text to the clipboard and raises a "copied" flag
// for FEEDBACK.copied — but only once the write has actually succeeded. When
// the clipboard is unavailable (non-secure context, permission denied) the
// flag stays down and copy() resolves false; the text on screen remains
// selectable for a manual copy.
//
// A surface with several copy buttons can pass a key to copy() and read
// copiedKey to tell which one to tick.
export function useCopyToClipboard<K extends string = string>() {
  const [copiedKey, setCopiedKey] = useState<K | "" | null>(null);
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);

  const copy = useCallback(async (text: string, key?: K): Promise<boolean> => {
    try {
      if (!navigator.clipboard) return false;
      await navigator.clipboard.writeText(text);
    } catch {
      return false;
    }
    window.clearTimeout(timer.current);
    setCopiedKey(key ?? "");
    timer.current = window.setTimeout(() => setCopiedKey(null), FEEDBACK.copied);
    return true;
  }, []);

  return { copied: copiedKey !== null, copiedKey, copy };
}
