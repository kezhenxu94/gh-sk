#!/usr/bin/env bash
# Build override for cli/gh-extension-precompile, run on a macOS runner: the
# keychain binding needs cgo for darwin; linux uses gopass and builds without cgo.
set -euo pipefail

mkdir -p dist
for arch in amd64 arm64; do
  clang_arch=$arch
  [[ $arch == amd64 ]] && clang_arch=x86_64
  GOOS=darwin GOARCH=$arch CGO_ENABLED=1 CC="clang -arch $clang_arch" \
    go build -trimpath -ldflags="-s -w" -o "dist/darwin-$arch" .
  GOOS=linux GOARCH=$arch CGO_ENABLED=0 \
    go build -trimpath -ldflags="-s -w" -o "dist/linux-$arch" .
done
