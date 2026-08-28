// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { createPortal } from "react-dom";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { BookOpen, LifeBuoy, X } from "lucide-react";
import { Button } from "../ui/Button";
import { useAuth } from "../../auth";
import { supportContactHref } from "../../lib/supportContact";
import { ICON } from "../../icons";
import { useEscapeToClose } from "../ui/useEscapeToClose";
import { DOCS } from "../../lib/externalLinks";

// HelpModal is what the header's "?" button and the "?" key open.
//
// It used to be shortcuts-only, which quietly failed the person who needed it
// most: "?" is the universally understood ASK-FOR-HELP affordance, so someone
// stuck on "what is a trigger" pressed it and got a table of accelerators.
// Meanwhile the documentation site had no link from anywhere in the app. So
// the modal now leads with the two things a stuck user actually wants — the
// docs and a human — and keeps the shortcuts below, where they cost a power
// user one extra glance and nothing else.

const isMac =
  typeof navigator !== "undefined" && /mac/i.test(navigator.platform);
const MOD = isMac ? "⌘" : "Ctrl";

type Shortcut = { keys: string[]; descKey: string };

const GENERAL: Shortcut[] = [
  { keys: [MOD, "K"], descKey: "shortcuts.commandBar" },
  { keys: ["?"], descKey: "shortcuts.help" },
];

const EDITOR: Shortcut[] = [
  { keys: [MOD, "K"], descKey: "shortcuts.addStep" },
  { keys: ["Del"], descKey: "shortcuts.deleteSelection" },
  { keys: ["←", "↑", "→", "↓"], descKey: "shortcuts.moveNode" },
];

function Row({ keys, desc }: { keys: string[]; desc: string }) {
  return (
    <div className="shortcut-row">
      <span className="shortcut-keys">
        {keys.map((k, i) => (
          <kbd key={i}>{k}</kbd>
        ))}
      </span>
      <span className="shortcut-desc">{desc}</span>
    </div>
  );
}

export function HelpModal({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { me } = useAuth();
  // Two routes to a human, in preference order: the in-app ticket thread when
  // the deployment runs support, otherwise whatever contact the operator
  // configured. Neither exists on a bare self-host, and then we render no
  // support row at all rather than a dead link.
  const supportOn = !!me?.support_tickets_enabled;
  const contactHref = supportContactHref(me?.support_contact);

  useEscapeToClose(onClose);

  return createPortal(
    <div className="modal-backdrop" onClick={onClose}>
      <div
        className="modal"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-modal="true"
        aria-label={t("help.title")}
      >
        <div className="modal-head">
          <h2>{t("help.title")}</h2>
          <Button
            variant="ghost"
            size="icon"
            onClick={onClose}
            aria-label={t("common.close")}
          >
            <X size={ICON.md} />
          </Button>
        </div>
        <div className="modal-body">
          <div className="help-links">
            <a
              className="help-link"
              href={DOCS}
              target="_blank"
              rel="noreferrer noopener"
            >
              <BookOpen size={ICON.md} className="help-link-icon" />
              <span className="help-link-text">
                <span className="help-link-title">{t("help.docs")}</span>
                <span className="help-link-desc">{t("help.docsDesc")}</span>
              </span>
            </a>
            {supportOn ? (
              <button
                type="button"
                className="help-link"
                onClick={() => {
                  onClose();
                  navigate("/support");
                }}
              >
                <LifeBuoy size={ICON.md} className="help-link-icon" />
                <span className="help-link-text">
                  <span className="help-link-title">{t("help.support")}</span>
                  <span className="help-link-desc">{t("help.supportDesc")}</span>
                </span>
              </button>
            ) : (
              contactHref && (
                <a
                  className="help-link"
                  href={contactHref}
                  {...(contactHref.startsWith("mailto:")
                    ? {}
                    : { target: "_blank", rel: "noreferrer noopener" })}
                >
                  <LifeBuoy size={ICON.md} className="help-link-icon" />
                  <span className="help-link-text">
                    <span className="help-link-title">{t("help.support")}</span>
                    <span className="help-link-desc">{t("help.supportDesc")}</span>
                  </span>
                </a>
              )
            )}
          </div>
          <h3 className="help-section">{t("shortcuts.title")}</h3>
          <div className="shortcut-group-label">{t("shortcuts.general")}</div>
          {GENERAL.map((s) => (
            <Row key={s.descKey} keys={s.keys} desc={t(s.descKey)} />
          ))}
          <div className="shortcut-group-label">{t("shortcuts.editor")}</div>
          {EDITOR.map((s) => (
            <Row key={s.descKey} keys={s.keys} desc={t(s.descKey)} />
          ))}
        </div>
      </div>
    </div>,
    document.body,
  );
}
