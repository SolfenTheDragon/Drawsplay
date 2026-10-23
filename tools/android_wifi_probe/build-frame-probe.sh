#!/usr/bin/env bash
set -euo pipefail
umask 077
if [[ $# -ne 1 || -e "$1" ]]; then
    echo "Usage: KOTLIN_STDLIB_JAR=/installed/kotlin-stdlib.jar $0 NEW_OUTPUT_DIRECTORY" >&2
    exit 1
fi
root="$(cd "$(dirname "$0")/../.." && pwd)"
compiled="$root/android/app/build/tmp/kotlin-classes/release"
sdk="${ANDROID_HOME:-${ANDROID_SDK_ROOT:-$HOME/android-sdk}}"
stdlib="${KOTLIN_STDLIB_JAR:?Select the installed Kotlin standard-library jar explicitly}"
if [[ ! -f "$stdlib" || ! -f "$compiled/com/drawsplay/tablet/transport/WiFiFrameCodec.class" ]]; then
    echo "Missing standard library or compiled app; run :app:compileReleaseKotlin first" >&2
    exit 1
fi
mkdir -m 700 -- "$1"
out="$(cd "$1" && pwd)"
mkdir "$out/classes" "$out/dex"
javac --release 8 -cp "$compiled:$stdlib" -d "$out/classes" \
    "$root/tools/android_wifi_probe/FrameProbe.java"
args=(cf "$out/probe.jar" -C "$out/classes" .)
for class in "$compiled"/com/drawsplay/tablet/transport/{WiFiFrameCodec,TlsSocketFactory}*.class; do
    args+=(-C "$compiled" "${class#"$compiled/"}")
done
jar "${args[@]}"
"$sdk/build-tools/36.0.0/d8" --min-api 26 \
    --lib "$sdk/platforms/android-34/android.jar" \
    --output "$out/dex" "$out/probe.jar" "$stdlib"
echo "Built isolated framing dex: $out/dex/classes.dex"
