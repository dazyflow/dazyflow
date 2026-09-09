// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useLayoutEffect, useRef, type ReactNode } from "react";

// JsonEditor is a dependency-free, syntax-highlighted JSON editor. It layers a
// transparent <textarea> (the real input + caret) over an aria-hidden <pre> that
// renders the same text tokenised into coloured spans. The two share identical
// metrics so the highlight sits exactly under the caret, and the textarea drives
// the <pre>'s scroll. No editor library — the stack has none and JSON tokenises
// with one regex.
//
// Tolerant by design: it highlights whatever currently looks like JSON, so
// mid-typing input still colours. `invalid` is a soft, caller-computed signal
// that only tints the border; it never blocks typing.
export function JsonEditor({
  value,
  onChange,
  rows = 8,
  placeholder,
  invalid,
}: {
  value: string;
  onChange: (v: string) => void;
  rows?: number;
  placeholder?: string;
  invalid?: boolean;
}) {
  const taRef = useRef<HTMLTextAreaElement>(null);
  const preRef = useRef<HTMLPreElement>(null);

  // Keep the highlight layer scrolled to match the textarea. useLayoutEffect so
  // it syncs before paint when the value changes programmatically (e.g. a
  // reference inserted from the {} menu), not just on user scroll.
  useLayoutEffect(() => {
    syncScroll();
  }, [value]);

  function syncScroll() {
    const ta = taRef.current;
    const pre = preRef.current;
    if (!ta || !pre) return;
    pre.scrollTop = ta.scrollTop;
    pre.scrollLeft = ta.scrollLeft;
  }

  return (
    <div
      className={"dz-json-editor" + (invalid ? " invalid" : "")}
      style={{ minHeight: `calc(${rows} * 1.5em + 16px)` }}
    >
      <pre ref={preRef} className="dz-json-pre" aria-hidden="true">
        {/* Tokens are React span elements (not an HTML string), so React
            escapes every value — no dangerouslySetInnerHTML, no XSS surface.
            Trailing newline keeps the last line visible when the value ends
            with \n (a textarea shows it; a <pre> would otherwise collapse it). */}
        <code>
          {tokenizeJSON(value)}
          {"\n"}
        </code>
      </pre>
      <textarea
        ref={taRef}
        className="dz-json-ta"
        spellCheck={false}
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        onScroll={syncScroll}
      />
    </div>
  );
}

// tokenizeJSON turns JSON text into React nodes — one <span> per coloured
// token, plain strings for the gaps — so React escapes every value as it renders
// and there is no innerHTML surface. Token groups:
//   1 = a string immediately followed by a colon → an object KEY
//   2 = the "<ws>:" that follows that key (ws kept plain, ":" punctuated)
//   3 = a string value
//   4 = a number
//   5 = true | false | null
//   6 = structural punctuation { } [ ] , :
// Strings are matched before numbers/literals, so digits inside a string are
// part of the string. Untokenised text is emitted verbatim.
export function tokenizeJSON(src: string): ReactNode[] {
  if (!src) return [];
  const re =
    /("(?:\\.|[^"\\])*")(\s*:)|("(?:\\.|[^"\\])*")|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|\b(true|false|null)\b|([{}[\],:])/g;
  const out: ReactNode[] = [];
  let last = 0;
  let key = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(src)) !== null) {
    if (m.index > last) out.push(src.slice(last, m.index));
    if (m[1] !== undefined) {
      const colon = m[2]; // e.g. "  :" — keep the whitespace plain, colour ":"
      out.push(
        <span key={key++} className="dz-j-key">
          {m[1]}
        </span>,
      );
      if (colon.length > 1) out.push(colon.slice(0, -1));
      out.push(
        <span key={key++} className="dz-j-punct">
          :
        </span>,
      );
    } else if (m[3] !== undefined) {
      out.push(
        <span key={key++} className="dz-j-string">
          {m[3]}
        </span>,
      );
    } else if (m[4] !== undefined) {
      out.push(
        <span key={key++} className="dz-j-number">
          {m[4]}
        </span>,
      );
    } else if (m[5] !== undefined) {
      out.push(
        <span key={key++} className="dz-j-bool">
          {m[5]}
        </span>,
      );
    } else {
      out.push(
        <span key={key++} className="dz-j-punct">
          {m[6]}
        </span>,
      );
    }
    last = re.lastIndex;
  }
  if (last < src.length) out.push(src.slice(last));
  return out;
}

export function isInvalidJSON(value: string): boolean {
  if (!value.trim()) return false;
  try {
    JSON.parse(value);
    return false;
  } catch {
    return true;
  }
}
