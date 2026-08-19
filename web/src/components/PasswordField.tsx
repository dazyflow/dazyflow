// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { forwardRef, useState, type InputHTMLAttributes } from "react";
import { useTranslation } from "react-i18next";
import { Eye, EyeOff } from "lucide-react";

// PasswordField is a password input with a reveal toggle.
//
// Every password box in the app was fully masked, including the sign-up pair
// (8+ characters, then a confirmation) — so the single most common reason a
// first sign-up fails is a typo the user is not allowed to see. One component
// rather than a toggle per page, so the three auth screens behave identically.
//
// The toggle is type="button" (never submits), carries an accessible label
// that names the ACTION rather than the state, and is excluded from the tab
// order: a keyboard user tabbing email → password → submit should not land on
// a decorative control between them. It stays reachable by click and by
// screen-reader navigation.
export const PasswordField = forwardRef<
  HTMLInputElement,
  InputHTMLAttributes<HTMLInputElement>
>(function PasswordField({ className, ...rest }, ref) {
  const { t } = useTranslation();
  const [shown, setShown] = useState(false);
  return (
    <div className="password-field">
      <input
        {...rest}
        ref={ref}
        type={shown ? "text" : "password"}
        className={className}
      />
      <button
        type="button"
        tabIndex={-1}
        className="password-reveal"
        onClick={() => setShown((v) => !v)}
        aria-label={shown ? t("common.hidePassword") : t("common.showPassword")}
        title={shown ? t("common.hidePassword") : t("common.showPassword")}
      >
        {shown ? <EyeOff size={16} /> : <Eye size={16} />}
      </button>
    </div>
  );
});
