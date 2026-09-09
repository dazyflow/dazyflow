// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  type TokenLabels,
  isSecretToken,
  tokenChipLabel,
  tokenizeValue,
} from "./nodeCardShared";

// TokenText renders a param value on a node card with every ${…} reference shown
// as a chip, worded the way the {} menu words it ("Gmail · Matching emails →
// first → id"), and the text around it left as text.
//
// Raw token syntax is never a thing to show a user: it is the wire format of a
// reference, not its name. The card already did this for a value that was
// ENTIRELY one token, so the shape the reference menu itself produces — a token
// inserted mid-sentence at the cursor — was exactly the shape that leaked the
// syntax.
//
// Read-only by design: text mixed with chips can't be edited in an <input>, and
// the Inspector already has the contenteditable field that can.
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
