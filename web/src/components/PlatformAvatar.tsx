import { isImageIcon } from "../lib/iconImage";

// PlatformAvatar renders the identity tiles used across the platform-admin
// moderation pages: a rounded-square OrgAvatar (uploaded logo or a tinted
// monogram) and a circular UserAvatar (a tinted monogram of the email).
// Both fall back to a deterministic colour derived from the seed, so the
// same org/user always gets the same hue — recognisable at a glance in a
// long list without needing a real photo.

// AVATAR_TINTS is a small palette of accessible, muted-but-distinct hues.
// White monogram text sits on all of them with comfortable contrast.
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

// tintFor hashes a seed to a stable palette index (djb2). Deterministic so
// an org/user keeps the same colour across reloads and pages.
function tintFor(seed: string): string {
  let h = 5381;
  for (let i = 0; i < seed.length; i++) h = (h * 33 + seed.charCodeAt(i)) >>> 0;
  return AVATAR_TINTS[h % AVATAR_TINTS.length];
}

// monogram takes the first 1–2 meaningful characters of a label, upper-cased.
function monogram(label: string): string {
  const trimmed = label.trim();
  if (!trimmed) return "?";
  // For an email, use the local part's first letter only.
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
