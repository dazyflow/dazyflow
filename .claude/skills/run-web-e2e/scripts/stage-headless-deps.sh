#!/usr/bin/env bash
# Stage the shared libraries + fonts that headless Chromium needs into
# /tmp, without root. For an Arch box with no sudo / no system browser.
# Idempotent: re-running just re-fetches and overwrites.
#
# After this, run the browser with:
#   LD_LIBRARY_PATH=/tmp/libs FONTCONFIG_FILE=/tmp/fonts/fonts.conf
set -euo pipefail

mkdir -p /tmp/libs /tmp/fonts /tmp/fontcache /tmp/pkgs
cd /tmp/pkgs

# Chromium's missing libs (discovered via `ldd chrome-headless-shell`).
# at-spi2-core provides libatk + libatk-bridge + libatspi on modern Arch.
LIB_PKGS="at-spi2-core libxcomposite libxdamage libxrandr libxkbcommon libxext libxrender libxi"

fetch() { # $1=pkg $2=arch(any|x86_64)
  local p="$1" arch="${2:-x86_64}"
  for repo in extra core; do
    if curl -sfL -o "$p.pkg.tar.zst" "https://archlinux.org/packages/$repo/$arch/$p/download/"; then
      mkdir -p "extract_$p"
      bsdtar -xf "$p.pkg.tar.zst" -C "extract_$p" 2>/dev/null || true
      return 0
    fi
  done
  echo "WARN: could not fetch $p" >&2
  return 1
}

for p in $LIB_PKGS; do
  fetch "$p" x86_64 && cp -a "extract_$p"/usr/lib/*.so* /tmp/libs/ 2>/dev/null || true
done

# A real text font + fontconfig (Skia FATALs without one).
fetch ttf-dejavu any && find "extract_ttf-dejavu" -name '*.ttf' -exec cp {} /tmp/fonts/ \; 2>/dev/null || true

cat > /tmp/fonts/fonts.conf <<'EOF'
<?xml version="1.0"?>
<!DOCTYPE fontconfig SYSTEM "fonts.dtd">
<fontconfig>
  <dir>/tmp/fonts</dir>
  <cachedir>/tmp/fontcache</cachedir>
</fontconfig>
EOF

echo "staged $(ls /tmp/libs/*.so* 2>/dev/null | wc -l) libs, $(ls /tmp/fonts/*.ttf 2>/dev/null | wc -l) fonts"
echo "verify with: LD_LIBRARY_PATH=/tmp/libs ldd <chrome-headless-shell> | grep 'not found'"
