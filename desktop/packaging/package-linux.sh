#!/bin/sh
# Builds a release tarball: dist/DrawSplayDesktop-linux-ARCH-vVERSION.tar.gz
# Usage: package-linux.sh [VERSION]   (default: the version in Cargo.toml; a leading "v" is ignored)
# Cross-build: TARGET=aarch64-unknown-linux-gnu package-linux.sh  (needs the target's linker and the
# arm64 libxdo, see BUILD notes in README.md). The file name then says aarch64.
set -eu
cd "$(dirname "$0")/.."
version=${1:-$(sed -n 's/^version = "\(.*\)"/\1/p' Cargo.toml | head -1)}
version=${version#v}
if [ -n "${TARGET:-}" ]; then
    cargo build --release --target "$TARGET"
    bin="${CARGO_TARGET_DIR:-target}/$TARGET/release"
    case "$TARGET" in aarch64-*) arch=aarch64 ;; x86_64-*) arch=x86_64 ;; *) arch=${TARGET%%-*} ;; esac
else
    cargo build --release
    bin="${CARGO_TARGET_DIR:-target}/release"
    arch=$(uname -m)
    [ "$arch" = arm64 ] && arch=aarch64
fi
name="DrawSplayDesktop-linux-${arch}-v${version}"
rm -rf "dist/${name}"
mkdir -p "dist/${name}"
cp "$bin/dd-host" "$bin/dd-client" "dist/${name}/"
# Documentation is optional (it may not be in the checkout).
if ls docs/*.md >/dev/null 2>&1; then mkdir -p "dist/${name}/docs" && cp docs/*.md "dist/${name}/docs/"; fi
cp README.md "dist/${name}/" 2>/dev/null || true
cp packaging/install-linux.sh packaging/99-drawsplay-desktop.rules "dist/${name}/"
cp ../LICENSE "dist/${name}/LICENSE" 2>/dev/null || true
chmod +x "dist/${name}/install-linux.sh"
tar -C dist -czf "dist/${name}.tar.gz" "${name}"
echo "dist/${name}.tar.gz"
