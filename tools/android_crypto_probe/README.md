# Isolated Android crypto probe

This developer-only Java/dex probe runs in an Android **shell process** via
ADB. It opens no sockets and installs no APK. It does not replace DrawSplay,
change settings, acquire locks, inject input or access screen contents.
Run it separately from USB baseline observations: it intentionally loads
the tablet CPU and would contaminate those measurements.

It uses platform `javax.crypto` and TLS capability APIs available on API 26.
Only the attached Android 14/Tab A8 has been physically tested; compilation
with `--min-api 26` is not Android 8 hardware verification.

The self-test checks the all-zero AES-128-GCM NIST known-answer vector,
decrypts it, rejects tampering, checks counter nonces, validates input bounds
and distinguishes unavailable transformations from successful measurement.
The probe reports AES-128-GCM and `ChaCha20/Poly1305/NoPadding`, where
supported by that device's provider. An unsupported transformation is
reported explicitly; initialization/execution errors fail the command.
Each run generates a fresh random key and uses unique 96-bit counter nonces.
These are throwaway test keys, never pairing/production keys.

For each algorithm and 1/16/64 KiB payload, perform 64 warm-up
encrypt/decrypt/byte-verification pairs, followed by a timed loop. Reuse
payload buffers; parameter objects and providers can allocate. The
`nsPerEncryptDecryptPair` field includes initialization, encryption,
decryption, plaintext verification and loop overhead. It is not pure
cipher cost. `payloadRoundtripsMbitPerSecond` is application payload
roundtrips, not TLS/wire throughput. Wall-clock timing includes scheduling
and can be disturbed by active DrawSplay, ADB and thermal behavior.

The default timed duration is 1000 ms per combination; an optional numeric
argument selects 1..5000 ms. These brief probes establish capability and
rough device-specific feasibility, not sustained performance, allocations,
power, foreground app cost, end-to-end latency or a cipher winner. The TLS
protocol list reports support only: **no TLS handshake or record overhead
is measured**. Follow with an actual bounded TLS transport experiment before
claiming Wi-Fi crypto performance.

## Reproduce

Use installed JDK 17, Android SDK/d8 and ADB. From repository root, select
a local workspace and device serial; do not publish serials:

```sh
work=$(mktemp -d)
mkdir "$work/classes" "$work/dex"
javac --release 8 -d "$work/classes" tools/android_crypto_probe/*.java
java -cp "$work/classes" com.drawsplay.benchmark.CryptoProbeTest
jar cf "$work/probe.jar" -C "$work/classes" .
"$ANDROID_HOME/build-tools/36.0.0/d8" --min-api 26 \
  --lib "$ANDROID_HOME/platforms/android-36/android.jar" \
  --output "$work/dex" "$work/probe.jar"

# Use a fresh named remote file; do not overwrite an unrelated file:
remote=$(adb -s TABLET_SERIAL shell mktemp /data/local/tmp/drawsplay-crypto.XXXXXXXX | tr -d '\r')
[[ "$remote" =~ ^/data/local/tmp/drawsplay-crypto\.[A-Za-z0-9]+$ ]] || exit 1
adb -s TABLET_SERIAL push "$work/dex/classes.dex" "$remote"
adb -s TABLET_SERIAL shell chmod 400 "$remote"
adb -s TABLET_SERIAL shell "CLASSPATH=$remote app_process /system/bin com.drawsplay.benchmark.CryptoProbeTest"
adb -s TABLET_SERIAL shell "CLASSPATH=$remote app_process /system/bin com.drawsplay.benchmark.CryptoProbe 1000"
adb -s TABLET_SERIAL shell rm "$remote"
```

The remote path returned by `mktemp` must match
`/data/local/tmp/drawsplay-crypto.[A-Za-z0-9]+` before interpolating it
into a shell command. In automation, validate it and remove that exact file
even on failure. Keep output local; it contains provider/protocol names and
timings, never test keys or plaintext data. Remove the named local class,
jar and dex files when finished.
