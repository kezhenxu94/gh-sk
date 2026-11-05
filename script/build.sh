#!/usr/bin/env bash
# Build override for cli/gh-extension-precompile: the keychain binding needs
# cgo, so only macOS binaries are built (on a macOS runner).
#
# When MACOS_SIGNING_IDENTITY is set (release workflow), binaries are signed
# with it under a fixed identifier, so the Keychain's "Always Allow" survives
# upgrades. Otherwise they keep the linker's ad-hoc signature.
set -euo pipefail

identifier=io.github.kezhenxu94.gh-sk

mkdir -p dist
for arch in amd64 arm64; do
  clang_arch=$arch
  [[ $arch == amd64 ]] && clang_arch=x86_64
  out="dist/darwin-$arch"
  GOOS=darwin GOARCH=$arch CGO_ENABLED=1 CC="clang -arch $clang_arch" \
    go build -trimpath -ldflags="-s -w" -o "$out" .

  if [[ -n "${MACOS_SIGNING_IDENTITY:-}" ]]; then
    codesign --force --sign "$MACOS_SIGNING_IDENTITY" --identifier "$identifier" \
      --timestamp --options runtime "$out"
    codesign --verify --strict --verbose=2 "$out"
    codesign --display --requirements - "$out"
  fi
done
