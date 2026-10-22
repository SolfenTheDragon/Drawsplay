# Isolated tablet advertisement probe

This temporary **separate APK** (`com.drawsplay.wifiprobe`) exercises Android
NSD advertisement and the host's new tablet-discovery command on real Wi-Fi.
It does not replace `com.drawsplay.tablet`, alter its data, decode a screen,
or inject input. Both listeners are bound before advertisement, but the
probe never accepts or reads application connections. The sockets exist
only to validate real advertised endpoint setup and cleanup.

The probe requires Android 13/API 33+ for network-scoped NSD registration.
On API 26–32 it reports the limitation and exits instead of advertising
across all networks. This is a test-tool limitation, not an assertion that
the planned product fallback for Android 8 is implemented.

## Build and run

Requires installed JDK 17, SDK platform 34 and build-tools 36.0.0, ADB and
Go 1.25. No downloads or dependency installation are performed.

```sh
bash tools/android_wifi_probe/build.sh /absolute/path/to/NEW-output
# Explicitly choose the attached testing tablet; keep its serial local.
adb -s TABLET_SERIAL install --no-streaming /absolute/path/to/NEW-output/probe.apk
adb -s TABLET_SERIAL shell am start -W \
  -n com.drawsplay.wifiprobe/.DiscoveryProbeActivity \
  --es address TABLET_WIFI_IPV4

cd host
go run ./cmd/wifi-discover -interface HOST_LAN_INTERFACE -duration 15s
```

The address must already belong to a Wi-Fi `Network` on the tablet; literal
IPv4 only, no DNS or process-wide network binding. NSD is scoped to that
network. The probe acquires a multicast lock only while active and advertises
`_drawsplay-tablet._tcp.` with `v=2`, the bound input port, and `videoPort`.
Ports are dynamically allocated, not the installed app's ports.
The optional `probe=1` attribute identifies synthetic test metadata but is
**not an authentication credential**.

It brings a temporary diagnostic activity to the foreground, which can
background the installed display app. Do not interpret resulting installed
app reconnects as a performance measurement or run this during other
measurements. No user interaction is required. It automatically finishes
after 40 seconds, unregisters NSD, closes sockets and releases its lock.

```sh
adb -s TABLET_SERIAL logcat -d -s DS-WiFiProbe:I '*:S'
adb -s TABLET_SERIAL uninstall com.drawsplay.wifiprobe
```

Only install if that separate package is absent; the build uses a new
disposable signing key each time and does not overwrite another installed
package. The private signing key is deleted by the build script. Do not
publish signing artifacts or device serials. Remove the exact generated
output files when no longer needed.

The host prints JSON lines for **untrusted candidates**; it never connects,
trusts an advertised pin, pairs, creates a monitor or starts capture.
Discovery success does not prove the ports accept authenticated sessions.
No result from this probe substitutes for integrated video, PAKE pairing,
API 26, IPv6, power or soak tests.

## Production Go/Kotlin framed-TLS experiment (no APK)

`FrameProbe.java` is a separate shell-process launcher for the actual
production `WiFiFrameCodec` and `TlsSocketFactory`. It first checks the
fixed wire fixture, then listens on an explicitly supplied tablet IPv4
address. A host with the supplied throwaway certificate pin initiates TCP
and proves its TLS server identity. Nine synthetic AU payloads are checked
byte-for-byte and echoed, reusing one 1 MiB array. It neither calls the
screen/decoder nor has an input-injection path.

Build release Kotlin classes, then set `KOTLIN_STDLIB_JAR` to the installed
Kotlin standard-library jar from your Gradle cache:

```sh
(cd android && ./gradlew :app:compileReleaseKotlin)
KOTLIN_STDLIB_JAR=/installed/kotlin-stdlib.jar \
  bash tools/android_wifi_probe/build-frame-probe.sh /absolute/NEW-frame-output
```

The script compiles only the probe and relevant production classes, then
dexes with min API 26. It installs no APK and opens no connection while
building. The measured device was API 34; min-API compilation is not
Android 8 runtime verification.

To reproduce on a trusted development tablet, generate a disposable P-256
certificate/key locally and compute the certificate DER SHA-256 hex pin.
Never use or export production signing/pairing keys. Push `dex/classes.dex`
to a fresh path made by:

```sh
adb -s TABLET_SERIAL shell mktemp /data/local/tmp/drawsplay-wifi-frame.XXXXXXXX
```

Validate the returned path against
`^/data/local/tmp/drawsplay-wifi-frame\.[A-Za-z0-9]+$` before substituting it
into any shell command. Make it mode 400, then start:

```sh
adb -s TABLET_SERIAL shell \
  "CLASSPATH=VALIDATED_REMOTE_PATH app_process /system/bin \
   com.drawsplay.wifiprobe.FrameProbe TABLET_IPV4 HOST_IPV4 TEST_CERT_PIN"
```

The process prints a fixture self-test followed by `readyTabletPort` and
waits at most 30 seconds for one TCP connection. While it waits, run:

```sh
cd host
go run ./cmd/wifi-frame-probe -interface HOST_LAN_INTERFACE \
  -tablet TABLET_IPV4 -port PRINTED_PORT \
  -cert /temporary/cert.pem -key /temporary/key.pem
```

Both endpoints must be on the selected link; check `ip route get TABLET_IPV4`
on Linux before testing. The host binds a source address from that interface.
This diagnostic is not the final route-enforced product transport; a
host with unusual policy/VPN routing needs explicit route verification.
The probe has a 90-second host deadline and 5-second tablet read timeout.
It exits after all nine verified frames. Delete exactly the remote dex
path and temporary certificate/key files afterwards, including on failure.
Supplying a test pin over trusted ADB is **not** the user-facing pairing flow.

See [`docs/wifi-wire.md`](../../docs/wifi-wire.md) and the recorded hardware
results. Production session integration and secure primary pairing remain
separate release gates.
