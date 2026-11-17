// True when `el` is somewhere the user types text (an input, textarea or
// contenteditable), so global single-key or Cmd shortcuts should stand down.
// Pass `e.target` from a key event, or `document.activeElement`.
export function isTypingTarget(el: EventTarget | Element | null | undefined): boolean {
  if (!el || typeof (el as HTMLElement).tagName !== "string") return false;
  const h = el as HTMLElement;
  return h.tagName === "INPUT" || h.tagName === "TEXTAREA" || h.isContentEditable === true;
}
