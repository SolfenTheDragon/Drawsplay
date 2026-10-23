#!/usr/bin/env bash
set -euo pipefail
umask 077
if [[ $# -ne 1 || -e "$1" ]]; then
    echo "Usage: $0 NEW_OUTPUT_DIRECTORY (must not exist)" >&2
    exit 1
fi
root="$(cd "$(dirname "$0")" && pwd)"
sdk="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/android-sdk}}"
build="$sdk/build-tools/36.0.0"
platform="$sdk/platforms/android-34/android.jar"
for tool in "$build/aapt2" "$build/d8" "$build/apksigner"; do
    if [[ ! -x "$tool" ]]; then
        echo "Missing installed SDK tool: $tool" >&2
        exit 1
    fi
done
if [[ ! -f "$platform" ]]; then
    echo "Missing installed SDK platform: $platform" >&2
    exit 1
fi
mkdir -m 700 -- "$1"
out="$(cd "$1" && pwd)"
trap 'rm -f -- "$out/signing.p12"' EXIT
mkdir "$out/classes" "$out/dex"
javac --release 8 -cp "$platform" -d "$out/classes" "$root/DiscoveryProbeActivity.java"
jar cf "$out/classes.jar" -C "$out/classes" .
"$build/d8" --min-api 26 --lib "$platform" --output "$out/dex" "$out/classes.jar"
"$build/aapt2" link -o "$out/unsigned.apk" --manifest "$root/AndroidManifest.xml" -I "$platform"
jar uf "$out/unsigned.apk" -C "$out/dex" classes.dex
keytool -genkeypair -alias wifi-probe -keyalg EC -groupname secp256r1 \
    -dname "CN=Temporary DrawSplay Wi-Fi Test" -validity 1 \
    -storetype PKCS12 -keystore "$out/signing.p12" \
    -storepass temporary-test-only -keypass temporary-test-only -noprompt \
    > "$out/signing.log" 2>&1
"$build/apksigner" sign --ks "$out/signing.p12" --ks-pass pass:temporary-test-only \
    --out "$out/probe.apk" "$out/unsigned.apk"
"$build/apksigner" verify "$out/probe.apk"
echo "Built isolated com.drawsplay.wifiprobe APK: $out/probe.apk"
