// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { SVGProps } from "react";
import { useId } from "react";

// GeminiIcon renders Google's Gemini spark. Shaped like a LucideIcon so it
// slots into the iconFor() registry, alongside ClaudeIcon, OpenAIIcon and
// OllamaIcon.
//
// It is a REDRAW of the mark, not the file. The published SVG
// (web/public/brands/gemini.svg, kept verbatim for the docs) paints the spark
// by masking a stack of eleven Gaussian-blurred colour blobs — a soft
// multi-hue glow at full size. Two reasons that file is not what this renders:
//
//   - At the 16-24px this component draws at, every blur radius is larger than
//     the icon. The glow collapses into a flat smear and costs eleven filter
//     primitives per instance to do it.
//   - Those filters and the mask are referenced by document-wide id. A React
//     component renders many times on one page (a palette, a node card, a
//     catalog row), and duplicate ids make the later copies resolve against
//     the first one's filters.
//
// So the outline path is the mask's own spark, filled with the same
// #4893FC → #969DFF → #BD99FE gradient the file's paint0 defines. The id that
// remains is scoped per instance with useId(), which is the same collision the
// second point describes, solved rather than avoided.
type Props = SVGProps<SVGSVGElement> & {
  size?: number | string;
  color?: string;
  strokeWidth?: number;
};

export function GeminiIcon({ size = 16, color, ...rest }: Props) {
  // useId() is per-instance and stable across hydration, so two icons on one
  // page never share a gradient.
  const gradientId = `gemini-spark-${useId()}`;
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size}
      height={size}
      viewBox="0 0 65 65"
      fill="none"
      role="img"
      aria-label="Gemini"
      {...rest}
    >
      <defs>
        <linearGradient
          id={gradientId}
          x1="18.447"
          y1="43.42"
          x2="52.153"
          y2="15.004"
          gradientUnits="userSpaceOnUse"
        >
          <stop stopColor="#4893FC" />
          <stop offset=".27" stopColor="#4893FC" />
          <stop offset=".777" stopColor="#969DFF" />
          <stop offset="1" stopColor="#BD99FE" />
        </linearGradient>
      </defs>
      {/* color, when a caller passes one, overrides the gradient — the
          monochrome contexts (a disabled row, a print sheet) need a flat fill. */}
      <path
        d="M32.447 0c.68 0 1.273.465 1.439 1.125a38.904 38.904 0 001.999 5.905c2.152 5 5.105 9.376 8.854 13.125 3.751 3.75 8.126 6.703 13.125 8.855a38.98 38.98 0 005.906 1.999c.66.166 1.124.758 1.124 1.438 0 .68-.464 1.273-1.125 1.439a38.902 38.902 0 00-5.905 1.999c-5 2.152-9.375 5.105-13.125 8.854-3.749 3.751-6.702 8.126-8.854 13.125a38.973 38.973 0 00-2 5.906 1.485 1.485 0 01-1.438 1.124c-.68 0-1.272-.464-1.438-1.125a38.913 38.913 0 00-2-5.905c-2.151-5-5.103-9.375-8.854-13.125-3.75-3.749-8.125-6.702-13.125-8.854a38.973 38.973 0 00-5.905-2A1.485 1.485 0 010 32.448c0-.68.465-1.272 1.125-1.438a38.903 38.903 0 005.905-2c5-2.151 9.376-5.104 13.125-8.854 3.75-3.749 6.703-8.125 8.855-13.125a38.972 38.972 0 001.999-5.905A1.485 1.485 0 0132.447 0z"
        fill={color ?? `url(#${gradientId})`}
      />
    </svg>
  );
}
