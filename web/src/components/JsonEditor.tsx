import { useLayoutEffect, useRef } from "react";

// JsonEditor is a dependency-free, syntax-highlighted JSON editor. It layers a
// transparent <textarea> (the real input + caret) over an aria-hidden <pre>
// that renders the same text tokenised into coloured spans. The two share
// identical metrics (font, padding, line-height, wrapping) so the visible
// highlight sits exactly under the caret; the textarea drives the <pre>'s
// scroll so they stay aligned. No editor library — the stack has none and JSON
// is simple enough to tokenise with one regex (see highlightJSON).
//
// Tolerant by design: it highlights whatever currently looks like JSON, so
// partial/mid-typing input still colours. Validity is a separate, soft signal
// — `invalid` (caller-computed) just tints the border red; it never blocks
// typing. Used by the inspector field and the on-card value-source editor.
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
      className={"hz-json-editor" + (invalid ? " invalid" : "")}
      style={{ minHeight: `calc(${rows} * 1.5em + 16px)` }}
    >
      <pre ref={preRef} className="hz-json-pre" aria-hidden="true">
        {/* Trailing newline keeps the last line visible when the value ends
            with \n (a textarea shows it; a <pre> would otherwise collapse it). */}
        <code dangerouslySetInnerHTML={{ __html: highlightJSON(value) + "\n" }} />
      </pre>
      <textarea
        ref={taRef}
        className="hz-json-ta"
        spellCheck={false}
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        onScroll={syncScroll}
      />
    </div>
  );
}

const ESC: Record<string, string> = { "&": "&amp;", "<": "&lt;", ">": "&gt;" };
const escapeHTML = (s: string) => s.replace(/[&<>]/g, (c) => ESC[c]);

// highlightJSON turns JSON text into HTML with one <span> per token. Groups:
//   1 = a string immediately followed by a colon → an object KEY
//   2 = the "<ws>:" that follows that key (ws kept plain, ":" punctuated)
//   3 = a string value
//   4 = a number
//   5 = true | false | null
//   6 = structural punctuation { } [ ] , :
// Strings are matched before numbers/literals, so digits/words inside a string
// are part of the string, not separately coloured. Untokenised text (whitespace,
// stray characters in mid-typing input) is escaped and emitted verbatim.
function highlightJSON(src: string): string {
  if (!src) return "";
  const re =
    /("(?:\\.|[^"\\])*")(\s*:)|("(?:\\.|[^"\\])*")|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|\b(true|false|null)\b|([{}[\],:])/g;
  let out = "";
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(src)) !== null) {
    out += escapeHTML(src.slice(last, m.index));
    if (m[1] !== undefined) {
      const colon = m[2]; // e.g. "  :" — keep the whitespace plain, colour ":"
      out +=
        `<span class="hz-j-key">${escapeHTML(m[1])}</span>` +
        escapeHTML(colon.slice(0, -1)) +
        `<span class="hz-j-punct">:</span>`;
    } else if (m[3] !== undefined) {
      out += `<span class="hz-j-string">${escapeHTML(m[3])}</span>`;
    } else if (m[4] !== undefined) {
      out += `<span class="hz-j-number">${escapeHTML(m[4])}</span>`;
    } else if (m[5] !== undefined) {
      out += `<span class="hz-j-bool">${escapeHTML(m[5])}</span>`;
    } else {
      out += `<span class="hz-j-punct">${escapeHTML(m[6])}</span>`;
    }
    last = re.lastIndex;
  }
  out += escapeHTML(src.slice(last));
  return out;
}

// isInvalidJSON reports whether non-empty text fails to parse — the soft red
// border signal. Empty/whitespace is treated as valid (nothing to flag yet).
export function isInvalidJSON(value: string): boolean {
  if (!value.trim()) return false;
  try {
    JSON.parse(value);
    return false;
  } catch {
    return true;
  }
}
