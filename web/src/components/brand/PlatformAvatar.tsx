// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { isImageIcon } from "../../lib/iconImage";
import { iconFor, dropColor, isBrandedIcon } from "../../icons";


const AVATAR_TINTS = [
  "#3b6fb5", // blue
  "#9b59b6", // purple
  "#2aa198", // teal
  "#d98324", // amber
  "#c0556b", // rose
  "#5a8f4e", // green
  "#7a6cc4", // indigo
  "#b4763c", // bronze
];

function tintFor(seed: string): string {
  let h = 5381;
  for (let i = 0; i < seed.length; i++) h = (h * 33 + seed.charCodeAt(i)) >>> 0;
  return AVATAR_TINTS[h % AVATAR_TINTS.length];
}

function monogram(label: string): string {
  const trimmed = label.trim();
  if (!trimmed) return "?";
  const base = trimmed.includes("@") ? trimmed.split("@")[0] : trimmed;
  return base.slice(0, 1).toUpperCase();
}

export function OrgAvatar({
  name,
  icon,
  seed,
  size = 36,
}: {
  name: string;
  icon?: string;
  seed: string;
  size?: number;
}) {
  if (isImageIcon(icon)) {
    return (
      <img
        src={icon}
        alt=""
        width={size}
        height={size}
        className="pa-avatar pa-avatar-org"
        style={{ objectFit: "contain" }}
        draggable={false}
      />
    );
  }
  return (
    <span
      className="pa-avatar pa-avatar-org pa-avatar-mono"
      style={{ width: size, height: size, background: tintFor(seed), fontSize: size * 0.42 }}
      aria-hidden="true"
    >
      {monogram(name || seed)}
    </span>
  );
}

export function DropGlyph({
  icon,
  category,
  color,
  brandLogo,
  size = 36,
}: {
  icon?: string;
  category?: string;
  color?: string;
  brandLogo?: string;
  size?: number;
}) {
  const Icon = iconFor(icon, category);
  const tint = dropColor(category, color);
  if (brandLogo) {
    return (
      <span
        className="pa-avatar pa-avatar-org pa-drop-glyph-plain"
        style={{ width: size, height: size }}
      >
        <img src={brandLogo} alt="" width={size * 0.66} height={size * 0.66} draggable={false} />
      </span>
    );
  }
  if (isBrandedIcon(icon)) {
    return (
      <span
        className="pa-avatar pa-avatar-org pa-drop-glyph-plain"
        style={{ width: size, height: size }}
      >
        <Icon size={size * 0.55} strokeWidth={2.2} />
      </span>
    );
  }
  return (
    <span
      className="pa-avatar pa-avatar-org"
      style={{
        width: size,
        height: size,
        background: `linear-gradient(135deg, ${tint}, color-mix(in srgb, ${tint} 70%, #fff))`,
      }}
    >
      <Icon size={size * 0.5} color="#140d30" strokeWidth={2.2} />
    </span>
  );
}

export function UserAvatar({
  email,
  size = 36,
}: {
  email: string;
  size?: number;
}) {
  return (
    <span
      className="pa-avatar pa-avatar-user pa-avatar-mono"
          style={{ width: size, height: size, background: tintFor(email), fontSize: size * 0.42 }}
      aria-hidden="true"
    >
      {monogram(email)}
    </span>
  );
}
