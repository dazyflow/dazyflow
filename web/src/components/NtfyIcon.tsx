import type { SVGProps } from "react";

// NtfyIcon renders the ntfy logo (https://ntfy.sh) — a teal chat bubble
// with a "> _" prompt mark. Simplified from the official SVG to keep
// the inlined markup compact and to avoid gradient-id collisions when
// multiple instances render in the same document.
type Props = SVGProps<SVGSVGElement> & {
  size?: number | string;
  color?: string;
  strokeWidth?: number;
};

export function NtfyIcon({ size = 16, color, ...rest }: Props) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size}
      height={size}
      viewBox="0 0 50 50"
      role="img"
      aria-label="ntfy"
      {...rest}
    >
      <rect width="50" height="50" rx="6" fill={color ?? "#52bca6"} />
      {/* Chat bubble outline */}
      <path
        d="M13.3 12.4c-2.4 0-4.5 1.9-4.5 4.3v18.9l-.6 4.5 8.3-2.2h20.6c2.4 0 4.5-1.9 4.5-4.3V16.7c0-2.4-2.1-4.3-4.5-4.3zm0 3.1h23.8c.85 0 1.44.62 1.44 1.28v16.85c0 .66-.59 1.27-1.44 1.27H16.05l-4.21 1.27.04-.25-.02-19.14c0-.66.59-1.28 1.44-1.28z"
        fill="#ffffff"
      />
      {/* "> _" prompt mark */}
      <path
        d="M19 22.8l4.5 2 -4.5 2v1.8l6.5-3v-1.6l-6.5-3zM27 30h7v1.5h-7z"
        fill="#ffffff"
      />
    </svg>
  );
}
