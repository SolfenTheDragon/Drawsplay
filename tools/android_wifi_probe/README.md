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
