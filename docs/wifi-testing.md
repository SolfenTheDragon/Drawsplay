# Wi-Fi component tests

USB baseline testing was stopped at the owner's request. These tests exercise
the Wi-Fi scaffold, discovery parser, framing and accepted-socket TLS primitive, not the completed
host-discovers-tablet design.
They do not install an APK, change the running host settings, or expose a LAN
listener. Test TLS listeners use loopback and temporary generated identities.

## Run

From `host/`:

```sh
go test -race -count=1 -v \
  ./internal/wifilan ./cmd/wifi-discover ./cmd/wifi-frame-probe
go vet ./internal/wifilan ./cmd/wifi-discover ./cmd/wifi-frame-probe
```

From `android/` with JDK 17:

```sh
./gradlew :app:testReleaseUnitTest \
  --tests 'com.drawsplay.tablet.transport.WiFiFrameCodecTest' \
  --tests 'com.drawsplay.tablet.transport.HostEndpointSelectorTest' \
  --tests 'com.drawsplay.tablet.transport.TlsSocketFactoryTest'
```

The Android TLS tests use the JDK's `keytool` to generate disposable ECDSA
identities in temporary directories. Keys and logs are deleted after each
test; no real credentials are used.

## Recorded result: 2026-10-22

The initial scaffold-focused selection completed successfully on Linux X11/JDK 17:

| Surface | Coverage |
| --- | --- |
| Go certificate handling | SHA-256 fingerprint, empty-chain rejection, persisted ECDSA identity and certificate hostname |
| Go TLS | TLS 1.2 and 1.3 over loopback TCP, matching pin accepted, wrong pin rejected |
| Existing Go Wi-Fi checks | Valid video token accepted; wrong token and oversized auth line rejected; disabled-feature settings preserved; API tokens redacted |
| Android endpoint selection | Credentials/ports retained for input and video; lost endpoint removed; changed address replaces stale candidate |
| Android TLS factory | Production factory authenticates before return; encrypted byte exchange; uppercase matching pin accepted; wrong pin rejected before return; stalled handshake times out and closes; malformed pins and unbounded timeouts rejected locally |

The Go selection contains eight top-level tests, including four TLS
subcases, and ran with the race detector. The Android selection contains
ten JVM tests. The Go TLS test uses a test client that verifies the
generated certificate pin; it is not an Android/Go interoperability test.

The four TLS regression tests were observed failing before the factory
change. Handshake timeouts limit read inactivity, not total elapsed time
against a peer that continually dribbles handshake bytes. No total wall-clock
handshake deadline is claimed.

The expanded commands above subsequently passed with 17 Android JVM tests
(seven framing, seven TLS, three endpoint selection), including 10,000
seeded malformed/truncated framing inputs. The Go Wi-Fi packages passed
race testing and vet; both diagnostic commands cross-compiled for Windows.
These results do not establish Windows multicast behavior.

## Not verified by these tests

The production TLS factory also passed a real Tab A8 LAN echo experiment
in both TCP directions, including host-initiated TCP with host TLS server
identity. See the [hardware report](performance/tab-a8-wifi-tls-2026-10-22.md).
This used an isolated shell process, not the installed app's streaming path.

Real multicast discovery is covered separately by the
[selected-interface discovery experiment](wifi-discovery.md), and synthetic
Go/Kotlin framed TLS by the [wire experiment](wifi-wire.md).
No claim is made about physical-tablet Wi-Fi display streaming,
pairing, general LAN-only route enforcement, reconnect under network changes,
decode performance, power or API 26 runtime compatibility. The attached Tab A8 was not used for the JVM/Go component run.

The existing scaffold still advertises the host and has the tablet discover
it. The new host-discovery command and framing codec are not wired into
the product's session lifecycle. Approved numeric-code pairing and
integrated LAN-only streaming remain implementation work.
Passing component tests is not a Phase 1 completion or a security
acceptance verdict.
