# Wi-Fi component tests

USB baseline testing was stopped at the owner's request. These tests exercise
the existing Wi-Fi scaffold, not the completed host-discovers-tablet design.
They do not install an APK, change the running host settings, or expose a LAN
listener. Test TLS listeners use loopback and temporary generated identities.

## Run

From `host/`:

```sh
go test -race -count=1 -v \
  -run 'Test(WiFi.*|SettingsAPIPreservesWiFiFieldsWhenFeatureDisabled|SettingsAndDiagnosticsRedactAuthTokens|AuthorizeTransportVideo.*)$' \
  ./...
```

From `android/` with JDK 17:

```sh
./gradlew :app:testReleaseUnitTest \
  --tests 'com.drawsplay.tablet.transport.HostEndpointSelectorTest' \
  --tests 'com.drawsplay.tablet.transport.TlsSocketFactoryTest'
```

The Android TLS tests use the JDK's `keytool` to generate disposable ECDSA
identities in temporary directories. Keys and logs are deleted after each
test; no real credentials are used.

## Recorded result: 2026-10-22

Both commands completed successfully on Linux X11/JDK 17:

| Surface | Coverage |
| --- | --- |
| Go certificate handling | SHA-256 fingerprint, empty-chain rejection, persisted ECDSA identity and certificate hostname |
| Go TLS | TLS 1.2 and 1.3 over loopback TCP, matching pin accepted, wrong pin rejected |
| Existing Go Wi-Fi checks | Valid video token accepted; wrong token and oversized auth line rejected; disabled-feature settings preserved; API tokens redacted |
| Android endpoint selection | Credentials/ports retained for input and video; lost endpoint removed; changed address replaces stale candidate |
| Android TLS factory | Real JVM TLS handshake and encrypted byte exchange using the production factory; uppercase matching pin accepted; wrong pin rejected |

The Go selection contains eight top-level tests, including four TLS
subcases, and ran with the race detector. The Android selection contains
five JVM tests. The Go TLS test uses a test client that verifies the
generated certificate pin; it is not an Android/Go interoperability test.

## Not verified by these tests

No claim is made about physical-tablet Wi-Fi streaming, discovery over
multicast, pairing, LAN-only routing, reconnect under network changes,
decode performance, power or API 26 runtime compatibility. The attached
Tab A8 was not used for this JVM/Go component run.

The existing scaffold still advertises the host and has the tablet discover
it. Host discovery of advertising tablets, approved numeric-code pairing,
and the new LAN-only session protocol remain implementation work.
Passing component tests is not a Phase 1 completion or a security
acceptance verdict.
