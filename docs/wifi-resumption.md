# Wi-Fi implementation resumption

Saved: 2026-10-22, at the owner's request to stop for the day.
Repository: `SolfenTheDragon/Drawsplay`.
Branch: `Wireless_Implementation`.

## Overall completion

**The implementation is incomplete. Phase 1 is still in progress.**
The installed normal app cannot yet be used as the requested wireless
secondary monitor. Do not mark the original task or Phase 1 complete.
There is no defensible overall percentage: secure connection components
are substantially implemented, but product integration, input and the
hardware/platform acceptance matrix remain considerable work.

| Scope | State |
| --- | --- |
| Phase 0 discovery/design | Written; owner authorized implementation. See `docs/wifi-design.md` and subsequent owner rulings below. |
| Host-discovered LAN enrollment | Committed and exercised on the actual Tab A8. |
| Numeric pairing, persistent trust, pinned mutual TLS | Committed, with Go/Kotlin tests and actual Keystore-backed LAN evidence. Normal-app pairing UI still absent. |
| Authenticated capability negotiation and separate control/video association | Committed; two complete actual-tablet fresh saved-trust channel checks passed. |
| Bounded Wi-Fi media reception and synthetic decoding | Implemented but **uncommitted and not ready to commit**. One successful 300-picture hardware run; three review findings remain open. |
| Normal-app wireless display, capture and touch/pen return | Not integrated. |
| Automatic reconnect, USB priority/handoff, normal lifecycle and diagnostics | Not complete. |
| Phase 2 direct connections/adaptive quality | Not complete. |
| Phase 3 impairment/fuzz/soak/lifecycle/platform hardening | Not complete; some component fuzz/negative tests exist, not full acceptance. |

## Owner rulings that must survive resumption

- Keep working on `Wireless_Implementation`; do not push or attach any
  Copilot co-author trailer to commits.
- **Test Wi-Fi, not USB.** The owner explicitly stopped USB testing.
  Camera/physical-interaction baseline requirements were waived earlier.
  Historical mandatory-baseline wording in the design does not authorize
  restarting paused USB tests.
- Preserve USB source, hot paths and behavior. Do not change shared USB
  decoder/pumps or introduce USB per-frame overhead to make Wi-Fi easier.
  Stop and ask before a change that risks USB behavior.
- All application Wi-Fi traffic stays on the selected LAN; no internet,
  cloud rendezvous or public DNS dependency.
- The **host discovers tablets and initiates both TCP connections**.
  It remains the TLS server; accepted tablet sockets act as TLS clients.
- Primary pairing is host autodetection plus a numeric code. QR is backup,
  not the primary interaction.
- API 26 is the minimum product target. The isolated hardware probe uses
  API-33 network-scoped NSD; that is **not** an API-26 availability solution.
- Use the attached Tab A8 when available. Do not replace its production app
  with a diagnostic APK. Isolated `.wifitest` installation must be absent
  before installation, and removed afterward.
- Be explicit about incomplete work and measured versus inferred results.
  The owner paused work for the day; do not continue device tests until resumption.

## Committed state

Last implementation commit before this handoff: `e824585`.

| Commit | Change |
| --- | --- |
| `c7b3825` | Numeric SRP pairing, persistent pinned identities and fail-closed missing-record handling. |
| `145af55` | Host-initiated selected-LAN enrollment, approval, durable trust and real-device fixes. |
| `04bf674` | Isolated LAN-enrollment automation documentation. |
| `7d354ea` | Bounded capabilities and one-use session tickets. |
| `e824585` | Paired control/video channel admission, saved-trust dialing, hardware automation/evidence. |

Earlier discovery/framing commits include `5713797` and `22a1cbe`.
No push or attribution trailer was added.

The committed session protocol uses a 96-byte Hello, bounded negotiated
AU/queue sizes, cryptographically random session IDs/epochs, a five-second
one-use video ticket bound to the control identity, and separate sequence
spaces. Exact fields are in `FUNCTIONALITY.md` section 3.8.

## Uncommitted work to preserve

New Android production files under
`android/app/src/main/java/com/drawsplay/tablet/video/`:

- `WiFiAccessUnitQueue.kt`: preallocated 2..4-slot AU pool, explicit
  writer/reader leases, byte/age bounds, dependency-safe drop-until-IDR
  recovery, overflow/age counters.
- `WiFiH264.kt`: baseline progressive SPS/PPS/config and AUD-delimited
  AU validation. **See open review findings before relying on it.**
- `WiFiVideoReceiver.kt`: paired epoch/sequence/config validation,
  direct receive into pool slots, negotiated size/duration checks,
  explicit bounded end record and interarrival counters.
- `WiFiVideoDecoder.kt`: separate synchronous MediaCodec owner,
  reusable input buffers, API-30 low-latency hint with basic fallback,
  output age drops, bounded flush/CSD recovery.

New corresponding JVM tests in `android/app/src/test/.../video/`:
`WiFiAccessUnitQueueTest.kt`, `WiFiH264Test.kt`,
`WiFiVideoReceiverTest.kt`.

Other changed/new Wi-Fi files:

- `transport/WiFiPairedSession.kt`: exposes the control epoch.
- `src/wifiTest/.../WiFiEnrollmentProbeActivity.kt`: synthetic Surface
  rendering, network reader and codec-owner thread, ready/result exchange.
- `host/cmd/wifi-pair/video.go`, `video_test.go`: bounded local DSVT
  synthetic fixture reader, paced sender, ready/end/result validation.
- `host/cmd/wifi-pair/main.go`, `main_test.go`, `session.go`:
  explicit `-test-video` option, implying paired-channel verification.
- `tools/wifi_enrollment/make_video_fixture.py`: generates FFmpeg lavfi
  test patterns, not desktop or camera capture, then packetizes AUs.
- `tools/wifi_enrollment/run_tablet_test.py`: optional `--test-video`
  flow and decode-result collection.

These changes are intentionally independent of USB. Do not discard them
or assume they have already been committed.

### Separate paused USB changes

Preserve but do not stage, test, revert or include with Wi-Fi commits:

```text
tools/usb_baseline/README.md
tools/usb_baseline/collect.py
tools/usb_baseline/test_workload.py
tools/usb_baseline/workload.py
```

## Findings and measured results

### Real synthetic Wi-Fi video

One successful actual Tab A8/API-34 run used:

| Metric | Result |
| --- | --- |
| Synthetic source | FFmpeg lavfi `testsrc2`, AVC constrained baseline, level 3.1 |
| Dimensions / target rate | 640x360 / 30 FPS |
| Pictures received | 300 |
| Pictures decoded | 300 |
| Pictures released to Surface | 300 |
| Queue drops / decoder drops | 0 / 0 |
| Queue overflow events / stale-age events | 0 / 0 |
| Compressed AU pool | 524,288 bytes, two 262,144-byte slots |
| Decoder | `OMX.sprd.h264.decoder` |
| Maximum local interarrival gap | 79,701 microseconds |

This was host discovery, numeric enrollment, fresh saved-trust control/video
mTLS, then actual H.264 receive/decode/release to a Surface over the LAN.
No desktop screen capture or input injection ran.
The test APK was removed; its absence was checked again when writing this
handoff. Disposable host identities were removed by the automation.

**Do not overclaim:** target FPS is not independently measured visible FPS.
Release-to-Surface is not proof of physical scanout, optical latency or
pen fidelity. The compressed pool is not total app/codec memory.
This short run is not power, thermal, soak, API-26, Windows, Wayland,
network-impairment or normal-app acceptance.

Raw successful report is in the session artifact directory as
`wifi-decode-tablet-result.json`. A portable factual copy is adjacent to
this document at
`performance/tab-a8-wifi-synthetic-video-2026-10-22.json`.

The raw report contains the legacy field
`"videoOrInputSessionStarted": false` even though synthetic video ran.
Interpret that only as no **product** display/input session; fix the
automation's ambiguous field name before recording future results.

### Bugs found during this batch

1. **Fresh clock sample race:** the decoder sampled time before the producer
   published an AU. The queue treated `receivedUs > sampledNowUs` as stale,
   incorrectly losing reference pictures and dropping dependent pictures
   until IDR. Before correction, runs decoded only 61/300 and 63/300.
   A deterministic test was observed failing, then the stale check was
   corrected to tolerate a producer publication after the consumer's clock
   sample. The 300/300 run followed.
2. **Immediate post-flush input assumption:** the actual codec did not return
   an input slot within the initial one-millisecond wait. The decoder now
   retains the recovery IDR, retries input nonblockingly for at most 500 ms,
   and resends cached CSD. **Forced-backlog recovery is not yet verified**;
   the successful clean run had zero overflows, so it does not prove this.
3. **Embedded parameter-set tail:** validation originally accepted a valid
   SPS followed by a hidden second SPS in its config field. A rejection test
   failed before adding single-NAL validation and passed afterward.
4. Some real LAN dials timed out before enrollment. Retrying after checking
   reachability allowed progress; the underlying intermittent connection
   issue is not diagnosed or considered fixed.

### Latest verification scope

- Committed channel milestone: 32 selected Android Wi-Fi transport JVM
  tests, zero failures/errors/skips; Go Wi-Fi race/vet; Windows
  cross-compilation; two real paired-channel runs.
- Current uncommitted media batch: latest Android run contains **12 media
  JVM tests**, zero failures/errors/skips: queue 6, H.264 3, receiver 3.
  This is not a combined fresh run of all 44 transport+media tests.
- The queue test measures 100,000 warmed acquire/publish/poll/release
  cycles and allows only fixed reflection-measurement overhead, not
  per-frame JVM objects. This is **not** whole-pipeline allocation proof
  on ART, Conscrypt, MediaCodec or Go TLS.
- Current host Wi-Fi packages/CLI passed targeted race testing and vet.
  The synthetic CLI tests cross-compiled for Windows; strict Windows
  LAN dialing still intentionally fails closed.
- No active shell jobs were listed at handoff. The existing product app
  and host were not stopped.

## Update 2026-10-23

The three review findings below were fixed test-first and the media batch
was committed (`4fb2ca4`, docs `917e0e0`, forced-recovery probe `ae6ce55`).
Steps 2-4, 6 (partly: FUNCTIONALITY, enrollment README, wifi-testing) and 7
are done; clean and forced-recovery tablet runs passed. Remaining: CPU/PSS/
allocation measurements, other design/pairing/wiki docs, and step 8
(normal-app integration). The text below is the historical finding record.

## Open review findings (historical; now fixed)

A read-only review of this uncommitted batch found three concrete issues.
**None has been fixed yet.** They are important correctness/resource
validation work even though the reviewer labeled them Medium.

1. **SPS coded-size and DPB validation**
   - File: `video/WiFiH264.kt`, around the SPS `max_num_ref_frames`,
     macroblock dimensions and crop validation.
   - Parser accepted a 2048x368 coded picture cropped to 640x360 despite
     negotiated maxima 1280x720. It checks visible dimensions but not
     sufficiently bounded coded dimensions.
   - Parser also accepted level-3.1 1280x720 with 16 reference frames:
     57,600 reference macroblocks exceeds level 3.1's 18,000-macroblock DPB.
   - Add failing tests for both. Reject excessive coded size (permit only
     supported alignment padding) and enforce level-specific decoded
     picture buffer limits before constructing MediaCodec.

2. **Mixed-picture slices in a single AU**
   - File: `video/WiFiH264.kt`, `inspect()`.
   - Increasing `first_mb_in_slice`, matching PPS and matching IDR flags
     do not prove all slices belong to the same picture.
   - Review combined first slice of P picture N with second slice of
     P picture N+1 under one AUD; `inspect()` accepted it.
   - Retain SPS/PPS syntax needed to parse and compare applicable picture
     identity: `frame_num`, POC, `idr_pic_id`, reference/nonreference
     status, etc. Continue rejecting unsupported interlaced/complex syntax.
   - Add a real differing-picture regression, not only repeated first
     slices or duplicate AUDs.
   - Last investigation command decoded the existing 640x360 fixture SPS:
     `seq_parameter_set_id=0`, `log2_max_frame_num_minus4=0`,
     `pic_order_cnt_type=2`, `max_num_ref_frames=1`.
     Thus its slices have four-bit frame numbers and no type-0 POC field.
     Existing tiny synthetic slice fixtures must be extended when the
     scanner begins validating those identity fields.

3. **End-of-stream loses a held recovery IDR**
   - File: `WiFiEnrollmentProbeActivity.kt`, decoder drain-loop condition;
     related state in `WiFiVideoDecoder.kt`.
   - A recovery IDR can be held in `pendingSlot` after flush while
     `pendingFrames == 0` and `queue.depth == 0`.
   - If explicit end arrives then, the loop can exit and `close()` releases
     that IDR without submitting it.
   - Expose work-in-progress/drained state including the held slot, and
     use that for shutdown. Add a deterministic or hardware-backed test
     where end arrives during post-flush input unavailability.

## Exact next steps

1. Read this handoff, `git status`, and relevant current files. Resume the
   uncommitted batch; do not reimplement the committed pairing/channel work.
2. Fix all three review findings using failing regression tests first.
   Inspect whole function boundaries after patches: earlier patches
   occasionally inserted a new test inside another function; ensure JUnit
   XML discovers the intended test methods.
3. Add test-only forced decoder/backlog recovery to the isolated synthetic
   probe and verify held-IDR/CSD recovery and end-of-stream draining.
   Keep failure thresholds honest; do not relax the existing synthetic
   >=90% release threshold merely to make a failed run green.
4. Rerun combined Wi-Fi transport+media JVM tests with the Go
   interoperability fixture configured. Check JUnit counts and zero skips.
   Rerun Wi-Fi-only Go race/vet, release Kotlin compile and isolated APK
   build; do not resume USB tests.
5. Rerun clean and forced-recovery real Tab A8 LAN media checks. Record
   before/after counts, failures and cleanup. Add real CPU/PSS/allocation
   measurements separately before making resource/latency claims.
6. Update `FUNCTIONALITY.md`, Wi-Fi design/testing/pairing docs,
   `tools/wifi_enrollment/README.md`, relevant wiki/build documentation
   and factual evidence for the **new** media config/AU/end/ready/result
   formats. These changes are not documented there yet.
7. Commit the reviewed Wi-Fi media work in focused commits, explicitly
   excluding paused USB files. No trailer and no push.
8. Continue Phase 1 product integration: normal tablet availability/UI,
   API-26 selected-network fallback, full tablet route/VPN enforcement,
   host inventory/settings, Windows strict-LAN dialing, real capture/
   encoder source, input/control pumps, automatic reconnect, diagnostics,
   foreground lifecycle/locks and USB-priority ownership from the Wi-Fi side.
   Any proposed change risking the existing USB path requires owner approval.
9. Only after actual Phase 1 acceptance, show results and stop at its gate.
   Phase 2/3 and the final implementation report remain future work.

## Reproduction commands

Run from the repository root unless `cd` is shown. Current tools:
Go 1.25, JDK 17, SDK at `/home/solfen/android-sdk`, ffmpeg with libx264.
The Go GOPATH=GOROOT warning is pre-existing.
Do not install dependencies unless a selected command reports them missing.

```sh
# Build the cross-language fixture and the opt-in host preview.
cd host
go build -o /ABSOLUTE/SESSION/wifi-pairing-test-peer ./cmd/wifi-pairing-test-peer
go build -o /ABSOLUTE/SESSION/wifi-pair ./cmd/wifi-pair
go test -race -count=1 ./internal/wifilan ./cmd/wifi-discover \
  ./cmd/wifi-frame-probe ./cmd/wifi-pairing-test-peer ./cmd/wifi-pair
go vet ./internal/wifilan ./cmd/wifi-discover ./cmd/wifi-frame-probe \
  ./cmd/wifi-pairing-test-peer ./cmd/wifi-pair

cd ../android
DRAWSPLAY_PAIRING_TEST_PEER=/ABSOLUTE/SESSION/wifi-pairing-test-peer \
./gradlew :app:testReleaseUnitTest \
  --tests 'com.drawsplay.tablet.transport.WiFi*Test' \
  --tests 'com.drawsplay.tablet.transport.HostEndpointSelectorTest' \
  --tests 'com.drawsplay.tablet.transport.TlsSocketFactoryTest' \
  --tests 'com.drawsplay.tablet.video.WiFi*Test' --console=plain
./gradlew :app:compileReleaseKotlin :app:assembleWifiTest --console=plain

cd ..
# Output must not already exist.
python3 tools/wifi_enrollment/make_video_fixture.py \
  --frames 300 --output /ABSOLUTE/SESSION/synthetic.dsvt
python3 tools/wifi_enrollment/run_tablet_test.py \
  --pair-command /ABSOLUTE/SESSION/wifi-pair \
  --apk android/app/build/outputs/apk/wifiTest/app-wifiTest.apk \
  --interface SELECTED_LAN_NIC \
  --test-video /ABSOLUTE/SESSION/synthetic.dsvt \
  --output /ABSOLUTE/SESSION/video-result.json
```

Select/revalidate current LAN addresses/interface after moving networks.
Previous test used `wlo1`, host `10.144.110.162/24`, tablet
`10.144.110.113/24`; these are historical, not fixed configuration.
Exactly one authorized attached tablet is required; do not persist its serial.

Session artifact directory:

```text
/home/solfen/.copilot/session-state/989a6dd2-77dd-40ae-8e24-7b21ebc79874/files/
```

Useful existing files there:
`wifi-synthetic.dsvt`, `wifi-synthetic.h264`,
`wifi-decode-tablet-result.json`, `wifi-pair`, `wifi-pairing-test-peer`,
`wifi-clock-race-red.log`, `wifi-clock-race-green.log`,
`wifi-hidden-sps-red.log`, `wifi-media-policies-final.log`,
`wifi-session-android-final.log`, `wifi-synthetic-apk.log`.
Rebuild executables after code changes; their current files are not an
automatic reflection of source edits.
