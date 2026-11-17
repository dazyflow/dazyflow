// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// True when `el` is somewhere the user types text (an input, a textarea or a
// contentEditable element), so single-key and clipboard shortcuts should leave
// the key alone. Pass `e.target` or `document.activeElement`.
export function isTypingTarget(el: EventTarget | null | undefined): boolean {
  if (!el || typeof (el as HTMLElement).tagName !== "string") return false;
  const h = el as HTMLElement;
  return h.tagName === "INPUT" || h.tagName === "TEXTAREA" || h.isContentEditable === true;
}
