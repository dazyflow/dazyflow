// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useLayoutEffect, useRef } from "react";
import { tokenizeScript, type ScriptLang } from "../../lib/scriptHighlight";

// ScriptEditor is the box a flow author writes a runner script in: a real
// textarea over an aria-hidden highlighted <pre>, the same two-layer trick as
// JsonEditor and CelInput, with the tokenizer chosen by the language the step
// says it will run the script with.
//
// It exists because the field was a single-line <input>. A script is many lines
// by nature — a shell one with a pipe per line, a Python one whose indentation
// IS its structure — and everything past the right edge was invisible: you
// could not read what a step would run without selecting the field and scrolling
// through it with the arrow keys.
//
// The highlight is generated as React elements rather than an HTML string, so
// React escapes every character of the script and there is no innerHTML to get
// wrong. That matters more here than in the JSON editor: this text is executed
// on someone's machine, and it arrives from a flow that may not be the reader's.
export function ScriptEditor({
  value,
  onChange,
  lang,
  rows = 10,
  placeholder,
}: {
  value: string;
  onChange: (v: string) => void;
  lang: ScriptLang;
  rows?: number;
  placeholder?: string;
}) {
  const taRef = useRef<HTMLTextAreaElement>(null);
  const preRef = useRef<HTMLPreElement>(null);

  // useLayoutEffect so the layers are aligned before paint when the value
  // changes programmatically (switching language, a reference inserted), not
  // only when the user scrolls.
  useLayoutEffect(() => {
    syncScroll();
  }, [value, lang]);

  function syncScroll() {
    const ta = taRef.current;
    const pre = preRef.current;
    if (!ta || !pre) return;
    pre.scrollTop = ta.scrollTop;
    pre.scrollLeft = ta.scrollLeft;
  }

  return (
    <div className="dz-code-editor" style={{ minHeight: `calc(${rows} * 1.5em + 16px)` }}>
      <pre ref={preRef} className="dz-code-pre" aria-hidden="true">
        {/* Trailing newline keeps the last line visible when the script ends
            with one — a textarea shows it, a <pre> would collapse it. */}
        <code>
          {tokenizeScript(value, lang).map((tok, i) =>
            typeof tok === "string" ? (
              tok
            ) : (
              <span key={i} className={"dz-s-" + tok.kind}>
                {tok.text}
              </span>
            ),
          )}
          {"\n"}
        </code>
      </pre>
      <textarea
        ref={taRef}
        className="dz-code-ta"
        // Belt and braces with `white-space: pre` in the stylesheet: this is the
        // HTML-level switch for soft wrapping, and it is unambiguous. A wrapped
        // line is a line the <pre> behind this has to break at the same
        // character, and two layout engines cannot be relied on to agree.
        wrap="off"
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        onScroll={syncScroll}
        onKeyDown={(e) => {
          // Tab indents instead of leaving the field. In a one-line input Tab
          // moving on is right; in a code box it is how you lose your place —
          // and Python is not writable without it. Shift+Tab still moves focus,
          // so the field is not a keyboard trap.
          if (e.key !== "Tab" || e.shiftKey) return;
          e.preventDefault();
          const ta = e.currentTarget;
          const { selectionStart: start, selectionEnd: end } = ta;
          onChange(value.slice(0, start) + "  " + value.slice(end));
          // The caret is restored after React has re-rendered with the new
          // value; setting it now would be overwritten by that render.
          requestAnimationFrame(() => {
            ta.selectionStart = ta.selectionEnd = start + 2;
          });
        }}
      />
    </div>
  );
}
