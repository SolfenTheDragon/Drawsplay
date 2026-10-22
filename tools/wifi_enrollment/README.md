# Actual-tablet LAN enrollment check

This opt-in check installs only the separate `com.drawsplay.tablet.wifitest`
APK, discovers its advertisement from the host, enters a fresh numeric code
in the test activity, approves on both endpoints, and verifies durable trust.
It never captures the desktop, injects input, replaces the display APK,
changes the running host settings, or tests USB.

Requires Linux, an explicit IPv4 LAN interface, Python 3, ADB, exactly one
authorized API-33+ tablet on the same LAN, and a visible/unlocked tablet.
The isolated probe's scoped NSD API requires 33; this does not establish
Android-8 availability support. ADB is used for test installation and local
UI automation only; enrollment traffic is host-initiated LAN TCP.

Build the host command from `host/` and the probe from `android/`:

```sh
go build -o /absolute/path/wifi-pair ./cmd/wifi-pair
./gradlew :app:assembleWifiTest
```

Then from the repository root:

```sh
python3 tools/wifi_enrollment/run_tablet_test.py \
  --pair-command /absolute/path/wifi-pair \
  --apk android/app/build/outputs/apk/wifiTest/app-wifiTest.apk \
  --interface wlo1 \
  --output /absolute/path/enrollment-result.json
```

It refuses to replace an existing isolated test installation. Pairing codes
are held only for test automation, not printed or saved in reports.
The script removes the isolated APK, its UI dump and disposable host trust
on success or failure. It verifies APK removal explicitly. Keep the tablet
unlocked while it runs; it does not bypass the lock screen.

Passing means discovery, numeric PAKE, actual Keystore mutual TLS, local
approval markers and both trust stores worked in that run. It is not a
wireless-monitor, automatic-reconnect, power/performance, VPN/route-change,
soak, Android-8 or Windows result. See `docs/wifi-pairing.md` for the
wire layout, routing restrictions and known limitations.

Add `--verify-session` to require two **new** host-initiated connections
after pairing, authenticated with the saved identities rather than another
code. It verifies bounded capability negotiation on control and a
five-second, one-use video attachment ticket over separate pinned mTLS.
Both endpoints confirm association before cleanup. This starts no capture,
decoder or input injector and does not establish automatic reconnect.
The JSON field `freshSavedTrustControlAndVideoVerified` records whether
this additional check ran.
