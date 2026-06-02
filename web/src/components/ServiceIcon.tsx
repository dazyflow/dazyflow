import type { ReactNode, SVGProps } from "react";

// ServiceIcon renders a third-party service's icon (sign-in providers,
// OAuth connectors). Where a service has its real logo wired up via the
// `glyph` field below it shows that; otherwise it falls back to a
// placeholder monogram tile. Both are real SVGs shaped like the
// lucide/brand icons elsewhere, so swapping a monogram for a logo never
// touches call sites — just add a `glyph` to the service's registry entry.
//
// The monogram fallback reads at a glance as a tinted rounded tile with
// the service's initial, which keeps still-unwired admin surfaces
// consistent and text-light: the icon carries the identity, not a label
// beside it.
//
// Two homes for brand art, by design — keep them separate:
//   • Inline `glyph` here — for the admin OAuth/SSO surfaces, where the
//     component renders inline (monogram fallback, `size`/props spreading,
//     no extra request for a tiny icon). New service icons go here.
//   • /public/brands/*.svg — URL-referenced logos for the connector drops
//     (a drop manifest's brandLogo points at one).
// Don't mirror a logo into both; pick the home that matches the surface.

type ServiceMeta = {
  label: string;
  tint: string; // monogram tile fill; monogram is always white for contrast
  mono: string; // 1–2 char monogram
  // Real brand logo. When present it replaces the monogram tile entirely;
  // `box` is the logo's own viewBox and `art` its (often multi-colour) paths.
  glyph?: { box: string; art: ReactNode };
};

// services keys are the stable identifiers the backend uses (OAuth
// provider `name`, SSO provider id). Tints lean on each brand's primary
// colour so the placeholder already feels right.
const services: Record<string, ServiceMeta> = {
  // OAuth connectors / sign-in
  google: {
    label: "Google",
    tint: "#1a73e8",
    mono: "G",
    glyph: {
      box: "0 0 128 128",
      art: (
        <>
          <path
            fill="#fff"
            d="M44.59 4.21a63.28 63.28 0 004.33 120.9 67.6 67.6 0 0032.36.35 57.13 57.13 0 0025.9-13.46 57.44 57.44 0 0016-26.26 74.33 74.33 0 001.61-33.58H65.27v24.69h34.47a29.72 29.72 0 01-12.66 19.52 36.16 36.16 0 01-13.93 5.5 41.29 41.29 0 01-15.1 0A37.16 37.16 0 0144 95.74a39.3 39.3 0 01-14.5-19.42 38.31 38.31 0 010-24.63 39.25 39.25 0 019.18-14.91A37.17 37.17 0 0176.13 27a34.28 34.28 0 0113.64 8q5.83-5.8 11.64-11.63c2-2.09 4.18-4.08 6.15-6.22A61.22 61.22 0 0087.2 4.59a64 64 0 00-42.61-.38z"
          />
          <path
            fill="#e33629"
            d="M44.59 4.21a64 64 0 0142.61.37 61.22 61.22 0 0120.35 12.62c-2 2.14-4.11 4.14-6.15 6.22Q95.58 29.23 89.77 35a34.28 34.28 0 00-13.64-8 37.17 37.17 0 00-37.46 9.74 39.25 39.25 0 00-9.18 14.91L8.76 35.6A63.53 63.53 0 0144.59 4.21z"
          />
          <path
            fill="#f8bd00"
            d="M3.26 51.5a62.93 62.93 0 015.5-15.9l20.73 16.09a38.31 38.31 0 000 24.63q-10.36 8-20.73 16.08a63.33 63.33 0 01-5.5-40.9z"
          />
          <path
            fill="#587dbd"
            d="M65.27 52.15h59.52a74.33 74.33 0 01-1.61 33.58 57.44 57.44 0 01-16 26.26c-6.69-5.22-13.41-10.4-20.1-15.62a29.72 29.72 0 0012.66-19.54H65.27c-.01-8.22 0-16.45 0-24.68z"
          />
          <path
            fill="#319f43"
            d="M8.75 92.4q10.37-8 20.73-16.08A39.3 39.3 0 0044 95.74a37.16 37.16 0 0014.08 6.08 41.29 41.29 0 0015.1 0 36.16 36.16 0 0013.93-5.5c6.69 5.22 13.41 10.4 20.1 15.62a57.13 57.13 0 01-25.9 13.47 67.6 67.6 0 01-32.36-.35 63 63 0 01-23-11.59A63.73 63.73 0 018.75 92.4z"
          />
        </>
      ),
    },
  },
  slack: {
    label: "Slack",
    tint: "#611f69",
    mono: "S",
    glyph: {
      box: "0 0 16 16",
      art: (
        <g fillRule="evenodd" clipRule="evenodd">
          <path
            fill="#E01E5A"
            d="M2.471 11.318a1.474 1.474 0 001.47-1.471v-1.47h-1.47A1.474 1.474 0 001 9.846c.001.811.659 1.469 1.47 1.47zm3.682-2.942a1.474 1.474 0 00-1.47 1.471v3.683c.002.811.66 1.468 1.47 1.47a1.474 1.474 0 001.47-1.47V9.846a1.474 1.474 0 00-1.47-1.47z"
          />
          <path
            fill="#36C5F0"
            d="M4.683 2.471c.001.811.659 1.469 1.47 1.47h1.47v-1.47A1.474 1.474 0 006.154 1a1.474 1.474 0 00-1.47 1.47zm2.94 3.682a1.474 1.474 0 00-1.47-1.47H2.47A1.474 1.474 0 001 6.153c.002.812.66 1.469 1.47 1.47h3.684a1.474 1.474 0 001.47-1.47z"
          />
          <path
            fill="#2EB67D"
            d="M9.847 7.624a1.474 1.474 0 001.47-1.47V2.47A1.474 1.474 0 009.848 1a1.474 1.474 0 00-1.47 1.47v3.684c.002.81.659 1.468 1.47 1.47zm3.682-2.941a1.474 1.474 0 00-1.47 1.47v1.47h1.47A1.474 1.474 0 0015 6.154a1.474 1.474 0 00-1.47-1.47z"
          />
          <path
            fill="#ECB22E"
            d="M8.377 9.847c.002.811.659 1.469 1.47 1.47h3.683A1.474 1.474 0 0015 9.848a1.474 1.474 0 00-1.47-1.47H9.847a1.474 1.474 0 00-1.47 1.47zm2.94 3.682a1.474 1.474 0 00-1.47-1.47h-1.47v1.47c.002.812.659 1.469 1.47 1.47a1.474 1.474 0 001.47-1.47z"
          />
        </g>
      ),
    },
  },
  github: {
    label: "GitHub",
    tint: "#2b3137",
    mono: "Gh",
    // GitHub's mark is monochrome — render it in currentColor (the ink
    // colour) so it stays legible in both light and dark themes rather
    // than the SVG's hardcoded white.
    glyph: {
      box: "0 0 98 96",
      art: (
        <path
          fill="currentColor"
          d="M41.4395 69.3848C28.8066 67.8535 19.9062 58.7617 19.9062 46.9902C19.9062 42.2051 21.6289 37.0371 24.5 33.5918C23.2559 30.4336 23.4473 23.7344 24.8828 20.959C28.7109 20.4805 33.8789 22.4902 36.9414 25.2656C40.5781 24.1172 44.4062 23.543 49.0957 23.543C53.7852 23.543 57.6133 24.1172 61.0586 25.1699C64.0254 22.4902 69.2891 20.4805 73.1172 20.959C74.457 23.543 74.6484 30.2422 73.4043 33.4961C76.4668 37.1328 78.0937 42.0137 78.0937 46.9902C78.0937 58.7617 69.1934 67.6621 56.3691 69.2891C59.623 71.3945 61.8242 75.9883 61.8242 81.252L61.8242 91.2051C61.8242 94.0762 64.2168 95.7031 67.0879 94.5547C84.4102 87.9512 98 70.6289 98 49.1914C98 22.1074 75.9883 6.69539e-07 48.9043 4.309e-07C21.8203 1.92261e-07 -1.9479e-07 22.1074 -4.3343e-07 49.1914C-6.20631e-07 70.4375 13.4941 88.0469 31.6777 94.6504C34.2617 95.6074 36.75 93.8848 36.75 91.3008L36.75 83.6445C35.4102 84.2188 33.6875 84.6016 32.1562 84.6016C25.8398 84.6016 22.1074 81.1563 19.4277 74.7441C18.375 72.1602 17.2266 70.6289 15.0254 70.3418C13.877 70.2461 13.4941 69.7676 13.4941 69.1934C13.4941 68.0449 15.4082 67.1836 17.3223 67.1836C20.0977 67.1836 22.4902 68.9063 24.9785 72.4473C26.8926 75.2227 28.9023 76.4668 31.2949 76.4668C33.6875 76.4668 35.2187 75.6055 37.4199 73.4043C39.0469 71.7773 40.291 70.3418 41.4395 69.3848Z"
        />
      ),
    },
  },
  notion: {
    label: "Notion",
    tint: "#2f2f2f",
    mono: "N",
    // Notion's mark is a self-contained white page with a black glyph, so
    // it reads on both themes (white tile, black N) without recolouring.
    glyph: {
      box: "0 0 100 100",
      art: (
        <>
          <path
            fill="#fff"
            d="M6.017 4.313l55.333 -4.087c6.797 -0.583 8.543 -0.19 12.817 2.917l17.663 12.443c2.913 2.14 3.883 2.723 3.883 5.053v68.243c0 4.277 -1.553 6.807 -6.99 7.193L24.467 99.967c-4.08 0.193 -6.023 -0.39 -8.16 -3.113L3.3 79.94c-2.333 -3.113 -3.3 -5.443 -3.3 -8.167V11.113c0 -3.497 1.553 -6.413 6.017 -6.8z"
          />
          <path
            fillRule="evenodd"
            clipRule="evenodd"
            fill="#000"
            d="M61.35 0.227l-55.333 4.087C1.553 4.7 0 7.617 0 11.113v60.66c0 2.723 0.967 5.053 3.3 8.167l13.007 16.913c2.137 2.723 4.08 3.307 8.16 3.113l64.257 -3.89c5.433 -0.387 6.99 -2.917 6.99 -7.193V20.64c0 -2.21 -0.873 -2.847 -3.443 -4.733L74.167 3.143c-4.273 -3.107 -6.02 -3.5 -12.817 -2.917zM25.92 19.523c-5.247 0.353 -6.437 0.433 -9.417 -1.99L8.927 11.507c-0.77 -0.78 -0.383 -1.753 1.557 -1.947l53.193 -3.887c4.467 -0.39 6.793 1.167 8.54 2.527l9.123 6.61c0.39 0.197 1.36 1.36 0.193 1.36l-54.933 3.307 -0.68 0.047zM19.803 88.3V30.367c0 -2.53 0.777 -3.697 3.103 -3.893L86 22.78c2.14 -0.193 3.107 1.167 3.107 3.693v57.547c0 2.53 -0.39 4.67 -3.883 4.863l-60.377 3.5c-3.493 0.193 -5.043 -0.97 -5.043 -4.083zm59.6 -54.827c0.387 1.75 0 3.5 -1.75 3.7l-2.91 0.577v42.773c-2.527 1.36 -4.853 2.137 -6.797 2.137 -3.107 0 -3.883 -0.973 -6.21 -3.887l-19.03 -29.94v28.967l6.02 1.363s0 3.5 -4.857 3.5l-13.39 0.777c-0.39 -0.78 0 -2.723 1.357 -3.11l3.497 -0.97v-38.3L30.48 40.667c-0.39 -1.75 0.58 -4.277 3.3 -4.473l14.367 -0.967 19.8 30.327v-26.83l-5.047 -0.58c-0.39 -2.143 1.163 -3.7 3.103 -3.89l13.4 -0.78z"
          />
        </>
      ),
    },
  },
  // SSO identity providers (placeholders for the ones not yet wired)
  microsoft: { label: "Microsoft Entra", tint: "#2f6fed", mono: "M" },
  okta: { label: "Okta", tint: "#007dc1", mono: "O" },
  saml: { label: "SAML", tint: "#5d09c7", mono: "SA" },
  oidc: { label: "OpenID Connect", tint: "#f7931e", mono: "ID" },
};

type Props = SVGProps<SVGSVGElement> & {
  name: string;
  size?: number | string;
};

export function serviceLabel(name: string): string {
  return services[name.toLowerCase()]?.label ?? name;
}

export function ServiceIcon({ name, size = 28, ...rest }: Props) {
  const key = name.toLowerCase();
  const meta: ServiceMeta = services[key] ?? {
    label: name,
    tint: "var(--surface-3)",
    mono: (name[0] ?? "?").toUpperCase(),
  };
  if (meta.glyph) {
    return (
      <svg
        xmlns="http://www.w3.org/2000/svg"
        width={size}
        height={size}
        viewBox={meta.glyph.box}
        role="img"
        aria-label={meta.label}
        {...rest}
      >
        {meta.glyph.art}
      </svg>
    );
  }
  const fontSize = meta.mono.length > 1 ? 12 : 16;
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size}
      height={size}
      viewBox="0 0 32 32"
      role="img"
      aria-label={meta.label}
      {...rest}
    >
      <rect width="32" height="32" rx="8" fill={meta.tint} />
      <text
        x="16"
        y="17"
        textAnchor="middle"
        dominantBaseline="central"
        fontFamily="var(--font-sans)"
        fontSize={fontSize}
        fontWeight={600}
        fill="#fff"
      >
        {meta.mono}
      </text>
    </svg>
  );
}
