# Vendored SheetJS + Excel drop generation

`xlsx.full.min.js` is the standalone [SheetJS](https://sheetjs.com) build,
**vendored on purpose** (not fetched at build time) so the daemon build stays
hermetic and works on a network-restricted builder. It is the input to the
Excel drops, not a generated artifact.

| | |
|---|---|
| Library | SheetJS Community Edition (`xlsx.full.min.js`, standalone) |
| Version | **0.20.3** |
| Source | https://cdn.sheetjs.com/xlsx-0.20.3/package/dist/xlsx.full.min.js |
| sha256 | `cc015130aa8521e7f088f88898eba949ccdcbfb38df0bd129b44b7273c3a6f41` |
| License | Apache-2.0 (© 2013-present SheetJS LLC) |

## How the Excel drops are built

`gen.go` bundles `xlsx.full.min.js` ahead of each drop body
(`excel_read.body.ts`, `excel_write.body.ts`) into one self-contained drop
source — `../excel_read.ts` / `../excel_write.ts` — which `officialdrops`
embeds and ships. Those two `.ts` files are **generated; do not edit them by
hand.** Edit the bodies here and regenerate:

```sh
go generate ./officialdrops        # from the repo root
```

That also regenerates `../manifests.json` (needs `node` on PATH).

## Updating SheetJS

Run `./update-sheetjs.sh`. It fetches the pinned version, verifies the sha256
above, and regenerates the drops. To move to a new release, bump `VERSION` +
`SHA256` in that script first.
