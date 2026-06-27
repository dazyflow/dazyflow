// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import type { SVGProps } from "react";

// GitIcon renders the official Git logo (CC-BY 3.0, Jason Long /
// git-scm.com). Shaped to match lucide-react's API so it can drop into
// the iconFor() registry alongside the rest. Fill is set to Git's
// canonical orange unless the caller overrides `color`.
type Props = SVGProps<SVGSVGElement> & {
  size?: number | string;
  color?: string;
  strokeWidth?: number;
};

export function GitIcon({ size = 16, color, ...rest }: Props) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size}
      height={size}
      viewBox="0 0 92 92"
      fill={color ?? "#F05033"}
      role="img"
      aria-label="Git"
      {...rest}
    >
      <path d="M90.155 41.965L50.036 1.847a5.83 5.83 0 0 0-8.246 0l-8.33 8.331 10.566 10.566a6.928 6.928 0 0 1 8.777 8.835l10.183 10.184a6.928 6.928 0 0 1 7.176 11.45 6.93 6.93 0 0 1-11.317-7.504L48.96 33.83v24.95a6.929 6.929 0 1 1-5.71-.2V33.402a6.93 6.93 0 0 1-3.762-9.089L29.07 13.892 1.847 41.112a5.833 5.833 0 0 0 0 8.247l40.118 40.119a5.83 5.83 0 0 0 8.246 0l39.943-39.942a5.832 5.832 0 0 0 0-8.246" />
    </svg>
  );
}
