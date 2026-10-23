# Wi-Fi component tests

USB baseline testing was stopped at the owner's request. These tests exercise
the Wi-Fi scaffold, discovery parser, pairing, framing and paired-channel
negotiation, not a completed wireless display.
The host/JVM commands below do not install an APK, change running host settings, or expose a LAN
listener. Test TLS listeners use loopback and temporary generated identities.

## Run

From `host/`:

```sh
go test -race -count=1 -v \
  ./internal/wifilan ./cmd/wifi-discover ./cmd/wifi-frame-probe ./cmd/wifi-pairing-test-peer ./cmd/wifi-pair
go vet ./internal/wifilan ./cmd/wifi-discover ./cmd/wifi-frame-probe ./cmd/wifi-pairing-test-peer ./cmd/wifi-pair
go build -o /tmp/drawsplay-wifi-pairing-test-peer ./cmd/wifi-pairing-test-peer
```

From `android/` with JDK 17:

```sh
DRAWSPLAY_PAIRING_TEST_PEER=/tmp/drawsplay-wifi-pairing-test-peer \
./gradlew :app:testReleaseUnitTest \
  --tests 'com.drawsplay.tablet.transport.WiFiPairingClientTest' \
  --tests 'com.drawsplay.tablet.transport.WiFiPairingInteropTest' \
  --tests 'com.drawsplay.tablet.transport.WiFiEnrollmentTest' \
  --tests 'com.drawsplay.tablet.transport.WiFiSession*Test' \
  --tests 'com.drawsplay.tablet.transport.WiFiPairedSessionTest' \
  --tests 'com.drawsplay.tablet.transport.WiFiMutualTlsTest' \
  --tests 'com.drawsplay.tablet.transport.WiFiTrustRecordCodecTest' \
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

## Later authentication/storage component checks

The [pairing components](wifi-pairing.md) add actual Go/Kotlin SRP
interoperability, mismatched-code/identity rejection, single-use/expiry/
attempt bounds, mutual TLS 1.2/1.3, rejection of absent/unpaired clients,
and encrypted trust-record tampering tests. A slow-dribble handshake
test was mutation-checked: delaying the absolute timeout made it fail,
and restoring the timeout made it pass. These checks are not enrollment UI
or integrated-session acceptance.

Host storage tests cover persistence without key rotation, revocation,
exclusive writers, bounded/corrupt/missing records, private Linux
permissions and symlink rejection. Windows implementation/tests
cross-compile; DPAPI and Windows ACL runtime behavior remain unverified.
The isolated `wifiTest` APK separately passed real Tab A8 Keystore
persistence, tamper rejection and non-exportable-key checks and was removed.

The consolidated selection passed 26 Android JVM tests with zero failures,
errors or skips, including the configured Go/Kotlin interoperability check.
Go Wi-Fi packages passed fresh race tests and vet. The missing-record
identity-rotation regression was observed failing before the store fix and
passing afterward.

The later network selection adds two Android test methods covering actual
host-initiated Go/Kotlin PAKE-to-mTLS TCP exchanges, wrong codes, invalid
versions, truncation and slow-dribble deadlines. Host tests additionally
cover transcript/key substitution, cancellation, invalid local identities,
strict on-link endpoint selection and the opt-in CLI argument boundary.
Actual Tab A8 enrollment is reproducible with
`tools/wifi_enrollment/run_tablet_test.py`; its factual
[result](performance/tab-a8-wifi-enrollment-2026-10-22.json) is separate from
streaming acceptance. DNS-SD instance names with escaped spaces prevented the
first selection attempt; decoding regression tests failed before the fix
and passed afterward. Hardware TLS exposed a missing Keystore raw-signing
permission not exercised by JVM identities; both hardware mTLS and a direct
Keystore prehashed-signature test passed after that fix.
The final expanded selection passed all 28 Android JVM tests without
failures, errors or skips, plus fresh Go Wi-Fi race/vet and Windows
cross-compilation. Two complete Tab A8 LAN enrollment runs passed after the
fixes. A separate real-device regression first demonstrated that saving
peer trust before requesting an identity stranded the record, then passed
after saves were made to initialize/validate the local identity first.

## Paired control/video association

The expanded selection passed **32 Android JVM tests**, with zero failures,
errors or skips, including production Go/Kotlin separate-channel mTLS,
capability negotiation, video attachment and a synthetic control ping.
Closing the Android control owner also closes video. Host tests cover
identity/role/resource-limit validation, random session ID/epoch reuse,
one-use/expired/wrong-identity attachment, malformed offer/attach payloads,
missing mTLS and cancellation during a partial header without reserving a
session. Wi-Fi-only Go race tests/vet and Windows cross-compilation passed;
this is not Windows LAN dialing/runtime evidence.

The optional hardware automation `--verify-session` passed on the API-34
Tab A8 after numeric enrollment, using saved trust on fresh control and
video connections. It validates negotiated bounds and association, not
capture/decoding or input injection. A first attempt encountered a LAN
connect timeout before enrollment. The missing tablet session accept path
then failed as expected; after integration, a redundant host socket close
incorrectly reported failure despite both-channel success. Fixing ownership
made the complete command pass, including trust, channel and cleanup checks.
See [recorded hardware evidence](performance/tab-a8-wifi-session-2026-10-22.json)
and `tools/wifi_enrollment/README.md` for reproduction.

## Synthetic media receive/decode (isolated preview)

Combined Wi-Fi transport and media JVM run: **48 tests, zero failures,
errors or skips** (Go interoperability fixture configured). Review fixes
were each made test-first: coded-size/DPB limits in the config SPS,
rejection of slices from different pictures in one access unit, and a
drain condition that waits for a held post-flush recovery IDR.

Actual Tab A8 (API 34, `OMX.sprd.h264.decoder`, 640x360 target 30 FPS,
FFmpeg `testsrc2` fixture, LAN after numeric enrollment and fresh saved-trust
channels):

| Run | Received | Decoded/released | Queue drops | Overflows |
| --- | --- | --- | --- | --- |
| Clean (`tab-a8-wifi-synthetic-video-2026-10-22.json`, before review fixes) | 300 | 300 | 0 | 0 |
| Clean, after review fixes | 300 | 300 | 0 | 0 |
| Forced recovery (`tab-a8-wifi-forced-recovery-2026-10-23.json`) | 300 | 270 | 30 | 1 |

Forced recovery stalls the probe decoder for 700 ms; the queue overflowed,
dropped dependent pictures until the next IDR and decoding resumed. The
end-of-stream-during-post-flush-hold case is covered by a pure drain-condition
unit test only, not by hardware. Release-to-Surface is not physical scanout,
latency, power, thermal, memory/allocation, soak, API 26, Windows or
normal-app evidence.

### Resource sample (900-picture run)

One 900-picture (30 s) synthetic run: 900 received, 900 decoded/released, no
drops or overflows (`performance/tab-a8-wifi-synthetic-video-900-2026-10-23.json`).
Host-side `adb shell` sampling of the **whole isolated probe process**
(1 s cadence, 28 samples over 46 s, which includes enrollment/TLS setup and
teardown, not only decoding): `dumpsys meminfo` total PSS ranged 70.9-89.5 MB;
cumulative process CPU time grew 0.83 s to 19.56 s, about 41% of one core
averaged over the span. These are coarse, single-run, probe-process numbers
(the sampling itself adds load); they are not steady-state decoder-only CPU,
allocation counts, power or thermal results.

## Not verified by these tests

The production TLS factory also passed a real Tab A8 LAN echo experiment
in both TCP directions, including host-initiated TCP with host TLS server
identity. See the [hardware report](performance/tab-a8-wifi-tls-2026-10-22.md).
This used an isolated shell process, not the installed app's streaming path.

Real multicast discovery is covered separately by the
[selected-interface discovery experiment](wifi-discovery.md), and synthetic
Go/Kotlin framed TLS by the [wire experiment](wifi-wire.md).
No claim is made about physical-tablet Wi-Fi display streaming,
normal-app pairing, general LAN-only route enforcement, reconnect under network changes,
decode performance, power or API 26 runtime compatibility. The attached Tab A8 was not used for the JVM/Go component run.

The existing scaffold still advertises the host and has the tablet discover
it. The new host-discovery command, pairing and channel negotiation are not
wired into the product's session lifecycle. Normal-app numeric-code pairing
and integrated LAN-only streaming remain implementation work.
Passing component tests is not a Phase 1 completion or a security
acceptance verdict.
