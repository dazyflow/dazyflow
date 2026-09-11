// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import { createPortal } from "react-dom";
import { useTranslation } from "react-i18next";
import { Maximize2, X } from "lucide-react";
import { Button } from "./Button";
import { ScriptEditor } from "./ScriptEditor";
import { useEscapeToClose } from "./useEscapeToClose";
import { ICON } from "../../icons";
import type { ScriptLang } from "../../lib/scriptHighlight";

// The same field, given the whole window.
//
// Some of what a flow author writes is long by nature — an HTML email layout, a
// script, a prompt — and the inspector is a column beside a canvas. Eight rows
// is the right size for the field in place and the wrong size for the twenty
// minutes spent writing what goes in it.
//
// It edits the SAME value, live: there is no draft to commit and no Cancel,
// because this is the field made bigger, not a dialog about the field. Closing
// it is closing a window, so nothing can be lost by closing the wrong one.
//
// `lang` decides which editor appears, and the caller decides `lang` — the same
// rule the field itself is rendered by, kept in one place rather than guessed at
// twice. Undefined means prose: a plain textarea, because a monospace font and
// syntax colours make an email body harder to read, not easier.
export function ExpandedEditor({
  title,
  value,
  lang,
  placeholder,
  onChange,
  preview,
}: {
  title: string;
  value: string;
  lang?: ScriptLang;
  placeholder?: string;
  onChange: (v: string) => void;
  /**
   * What to show beside the editor: a rendered picture of what is being
   * written, where the field has one. A slot rather than a feature, because
   * what "preview" means is different for every field that has one — markup
   * renders, a script does not — and a host that knew would have to learn
   * again for each.
   */
  preview?: React.ReactNode;
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);

  return (
    <>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        icon={<Maximize2 size={ICON.xs} />}
        onClick={() => setOpen(true)}
        title={t("schemaForm.expandHint", { field: title })}
      >
        {t("schemaForm.expand")}
      </Button>
      {open && (
        <Overlay
          title={title}
          value={value}
          lang={lang}
          placeholder={placeholder}
          onChange={onChange}
          preview={preview}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  );
}

// The window itself, mounted only while it is open — so the Escape listener
// belongs to the dialog on screen rather than to every expandable field on the
// form, of which an inspector holds several.
function Overlay({
  title,
  value,
  lang,
  placeholder,
  onChange,
  preview,
  onClose,
}: {
  title: string;
  value: string;
  lang?: ScriptLang;
  placeholder?: string;
  onChange: (v: string) => void;
  preview?: React.ReactNode;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  useEscapeToClose(onClose);

  return createPortal(
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal dz-bigedit"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label={title}
      >
        <div className="modal-head">
          <h2>{title}</h2>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            onClick={onClose}
            title={t("common.close")}
            aria-label={t("common.close")}
          >
            <X size={ICON.md} />
          </Button>
        </div>
        <div className={"modal-body dz-bigedit-body" + (preview ? " dz-bigedit-split" : "")}>
          {/* The editor gets a pane of its own so the two halves are sized by
              one rule rather than by whichever of editor and preview carries
              the more specific selector. */}
          <div className="dz-bigedit-pane">
          {lang ? (
            <ScriptEditor
              value={value}
              lang={lang}
              onChange={onChange}
              placeholder={placeholder}
              autoFocus
            />
          ) : (
            <textarea
              className="dz-bigedit-plain"
              value={value}
              placeholder={placeholder}
              onChange={(e) => onChange(e.target.value)}
              autoFocus
            />
          )}
          </div>
          {preview && <div className="dz-bigedit-preview">{preview}</div>}
        </div>
      </div>
    </div>,
    document.body,
  );
}
