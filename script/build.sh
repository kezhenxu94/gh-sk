#!/usr/bin/env bash
# Build override for cli/gh-extension-precompile: the keychain binding needs
# cgo, so only macOS binaries are built (on a macOS runner).
set -euo pipefail

mkdir -p dist
for arch in amd64 arm64; do
  clang_arch=$arch
  [[ $arch == amd64 ]] && clang_arch=x86_64
  GOOS=darwin GOARCH=$arch CGO_ENABLED=1 CC="clang -arch $clang_arch" \
    go build -trimpath -ldflags="-s -w" -o "dist/darwin-$arch" .
done
