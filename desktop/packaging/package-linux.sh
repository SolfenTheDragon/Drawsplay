#!/bin/sh
# Builds a release tarball: dist/DrawSplayDesktop-linux-ARCH-vVERSION.tar.gz
# Usage: package-linux.sh [VERSION]   (default: the version in Cargo.toml; a leading "v" is ignored)
# Cross-build: TARGET=aarch64-unknown-linux-gnu package-linux.sh  (needs the target's linker and the
# arm64 libxdo, see "DrawSplay Desktop on ARM" in the repository BUILD.md). The file name then says aarch64.
set -eu
cd "$(dirname "$0")/.."
version=${1:-$(sed -n 's/^version = "\(.*\)"/\1/p' Cargo.toml | head -1)}
version=${version#v}
# Built into dd-host, which reports it in its status file (unset: "dev").
if [ -n "${1:-}" ]; then
    export DRAWSPLAY_VERSION="v$version"
fi
if [ -n "${TARGET:-}" ]; then
    cargo build --release --locked --target "$TARGET"
    bin="${CARGO_TARGET_DIR:-target}/$TARGET/release"
    case "$TARGET" in aarch64-*) arch=aarch64 ;; x86_64-*) arch=x86_64 ;; *) arch=${TARGET%%-*} ;; esac
else
    cargo build --release --locked
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
cp README.md GUIDE.md ../LICENSE ../THIRD-PARTY-NOTICES.md "dist/${name}/"
mkdir -p "dist/${name}/drivers/windows/idd"
cp ../drivers/windows/idd/LICENSE "dist/${name}/drivers/windows/idd/LICENSE"
cp packaging/install-linux.sh ../packaging/linux/99-drawsplay-uinput.rules "dist/${name}/"
chmod +x "dist/${name}/install-linux.sh"
tar -C dist -czf "dist/${name}.tar.gz" "${name}"
echo "dist/${name}.tar.gz"
