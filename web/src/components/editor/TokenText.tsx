// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  type TokenLabels,
  isSecretToken,
  tokenChipLabel,
  tokenizeValue,
} from "./nodeCardShared";

// TokenText renders a param value on a node card with every ${…} reference
// shown as a chip, the way the {} menu words it ("Gmail · Matching emails →
// first → id"), and the text around it left as text.
//
// Raw token syntax is never a thing to show a user: it is the wire format of a
// reference, not its name, and the card is the surface people read a flow
// from. The card already did this for a value that was ENTIRELY one token —
// but a value with a token in the middle of a sentence fell through to plain
// text, so the shape the reference menu itself produces ("Deadline:
// ${upstream.date_1.out}", inserted at the cursor) was exactly the shape that
// leaked the syntax.
//
// Read-only by design. Text mixed with chips can't be edited in an <input>,
// and the Inspector already has the contenteditable field that can; the card
// shows what the value IS and sends you there to change it.
export function TokenText({
  value,
  labels,
}: {
  value: string;
  labels?: TokenLabels;
}) {
  return (
    <>
      {tokenizeValue(value).map((seg, i) =>
        seg.kind === "text" ? (
          // Fragment keyed per segment: the same text can appear twice in one
          // value ("from ${a} to ${a}"), so the index is the only stable key.
          <span key={i}>{seg.text}</span>
        ) : (
          <span
            key={i}
            className={
              "dz-token-chip" +
              (isSecretToken(seg.token) ? " dz-token-chip-secret" : "")
            }
            title={seg.token}
          >
            <span className="dz-token-chip-text">
              {tokenChipLabel(seg.token, labels)}
            </span>
          </span>
        ),
      )}
    </>
  );
}
