# Wi-Fi work: USB baseline readiness and hardware observations

Date: 2026-10-22. Branch: `Wireless_Implementation`.

**Acceptance baseline: NOT ESTABLISHED. USB non-regression: NOT PROVEN.**
These are passive observations of the attached tablet, not a repeatable
end-to-end benchmark, a before/after comparison, or permission to implement
Wi-Fi. No host/app source, settings, installed binaries, firewall rules,
network configuration, or USB framing were changed.

## 1. Build and hardware identity

| Item | Observed value |
| --- | --- |
| Source checkout | `fe52f8592f4efac60d137dde297c0a1573142a80` |
| Running host | `dev-76837e1`, embedded revision `76837e1eead65c823b34e97198c4eedf49112048`, `vcs.modified=true` |
| Running host SHA-256 | `841de7e05058346697034da41981d0ad2ffcc30db15893ae8f64f231ca0ddb19` |
| Host runtime | Linux `7.0.0-38-generic`, X11 |
| Tablet | Samsung SM-X200, Galaxy Tab A8, Unisoc/Spreadtrum T618 |
| Android | Android 14, API 34, arm64-v8a supported |
| Installed app | `com.drawsplay.tablet`, version 0.2.0, versionCode 2, minSdk 26, targetSdk 36 |
| Tablet physical RAM | `2,534,920 kB` from `/proc/meminfo`, about 2.42 GiB |
| CPU crypto capabilities | `/proc/cpuinfo` advertises `aes`, `pmull`, `sha1`, `sha2` |
| Physical USB link | `18d1:2d01`, sysfs speed `480` Mbit/s; this is bus signaling speed, NOT measured useful throughput |
| Application transport | Host reports AOA `linked`, AOA enabled, ADB tunnel disabled, Wi-Fi feature disabled |
| Active video | X11 extended `Virtual-1-1`, 1200x800, H.264 VA-API, requested 60 fps, configured 11111 kbit/s |
| Tablet panel / recommendation | Native 1920x1200 at 60 Hz; app recommends at most 1280x800 for its RAM tier |

The running binary is **not a reproducible build of the checkout**. The APK's
source revision and hash were not established. Their version strings do not
identify exact source equivalence. Do not compare these observations to a
fresh checkout build and attribute a difference to Wi-Fi.

ADB was started for read-only inspection; it is not the application transport.
On this Linux setup the composite accessory identity exposes ADB as well as
AOA. This does not establish that ADB remains accessible with every Windows
WinUSB binding. ADB sampling shares the USB device and can perturb it.

## 2. Recorded measurements and validity

Raw, generated command output:

The text files retain the commands' original output, including terminal
padding/trailing spaces; they have not been whitespace-normalized.

- [First host observation](wifi-phase0-host-observation-2026-10-22.txt):
  14:09:41-14:10:41 UTC.
- [Concurrent tablet observation](wifi-phase0-tablet-observation-2026-10-22.txt):
  approximately the same minute; exact timestamps are in the file.
- [Host retry](wifi-phase0-host-retry-2026-10-22.txt):
  14:13:10-14:14:10 UTC, targeting the then-current encoder PID.

`top` took 31 samples two seconds apart. Discard the first sample; the
remaining 30 report interval CPU usage. Percentages below are **one logical
core equivalents** (100% = one core), not percentages of the entire machine.
These are means of rounded `top` readings, not independently replicated runs.

| Metric | Observation | Interpretation |
| --- | --- | --- |
| Host service CPU, first minute | Mean 5.76%, range 4.0-14.5% | Contains a session restart; not steady-state acceptance evidence |
| Encoder CPU, first minute | Unavailable | Previously observed encoder PID had exited; zero samples, NOT zero CPU |
| Host service CPU, retry | Mean 4.57%, median 4.5%, range 4.0-5.0% | 30 interval samples, unchanged restart counters during this minute |
| Encoder CPU, retry | Mean 10.60%, median 10.5%, range 10.0-11.9% | Encoder PID 34840 remained present at both boundaries |
| Tablet application CPU, first minute | Mean 54.95%, range 41.0-87.5% | Different window from valid host retry; includes restart/catch-up |
| Tablet PSS, first minute | 53,355 -> 54,354 kB | Two snapshots, not a leak/soak conclusion |
| Tablet RSS, first minute | 106,272 -> 107,764 kB | Two snapshots; includes shared mappings |
| Tablet swap PSS, first minute | 19,582 -> 19,558 kB | Low RSS alone does not describe all memory pressure |
| Battery proxy, first minute | USB powered/charging, 23%, 3791 mV, `current now: 217000`, 27.4 C | Cached/vendor current reading; sign/units/update cadence not independently calibrated |
| Full end-to-end / photon latency | Not measured | No camera/photodiode or controlled presentation workload |
| Sustained useful FPS / unique-frame drop rate | Not measured | No controlled source sequence or complete stats history |
| Input application RTT / input-to-photon | Not measured | No controlled input target; current FFmpeg path sends no input echo metadata |
| Crypto overhead on tablet | Not measured | CPU capability flags are not throughput, latency, allocation, or power measurements |

### A disturbance makes the first window unsuitable

The first host sample began with `framesReceived=171`, `framesRendered=3`,
`queuedFrames=45`, `staleFrames=130` in the latest approximately 250 ms report.
During that minute host counters changed:

| Counter | Before | After |
| --- | --- | --- |
| Video connections | 3 | 4 |
| Video restarts | 2 | 3 |
| Capture restarts | 5 | 6 |
| Input connections / restarts | 1 / 0 | 1 / 0 |

The cause was **not investigated or assigned**. No restart request or
settings mutation was made by this discovery work. This might involve the
existing application, outside activity, or measurement perturbation; the
available observations do not distinguish them. It is not evidence that
Wi-Fi caused a regression: no Wi-Fi changes were running.

The retry retained counters 4 / 3 / 6 throughout and observed both processes,
but still lacked a controlled workload and replicated trials.

### Do not reinterpret existing telemetry as a full benchmark

- Android reports deltas approximately every 250 ms; `/api/status` exposes
  only the latest report, without a unique report ID. Polling faster can
  duplicate a report; polling slower loses reports. Never sum snapshots as
  if each were a new interval.
- `framesReceived` counts VCL NALs, including individual slices, rather than
  unique source pictures. `framesRendered` counts codec output buffers
  released for presentation, not confirmed photons.
- In the valid retry, both boundary snapshots show 16 rendered outputs and
  15 received VCL NALs in 250 ms. This reflects interval accounting and
  pipeline timing, not proof of a sustained 64 fps on a 60 Hz panel.
- `avgDecodeMs` is codec-input timestamp to output callback delay, including
  queueing. Values near 26 ms here are not capture-to-screen latency.
- `queuedFrames` is pending NAL entries, excluding the codec's internal
  pictures. `frameDrops` is a pacing-resynchronization counter;
  `staleFrames` includes several kinds of intentional discard. Neither
  alone gives a unique-frame loss rate.
- The optional native helper echoes input sequence metadata; the active
  plain Annex-B FFmpeg pipeline does not. Even the helper echo ends at
  metadata receipt and does not establish OS application response or panel
  presentation.
- A charging battery's net current cannot be presented as tablet/app power
  draw. Direct sysfs current/voltage access was denied. No discharging or
  calibrated USB-meter experiment was performed.

## 3. Reproducing these limited observations

Use a trusted debugging connection only for measurement. Select the tablet
serial locally; do not publish it. These commands do not implement a
benchmark harness and do not establish the missing metrics:

```sh
git rev-parse HEAD
go version -m /absolute/path/to/running/drawsplay-host
sha256sum /absolute/path/to/running/drawsplay-host
curl --max-time 5 -fsS http://127.0.0.1:27180/api/status
ps -eo pid,ppid,comm,args
# Replace the two PIDs with the verified live service and capture PIDs:
top -b -n 31 -d 2 -p HOST_PID,ENCODER_PID
adb -s TABLET_SERIAL shell pidof com.drawsplay.tablet
adb -s TABLET_SERIAL shell dumpsys meminfo com.drawsplay.tablet
adb -s TABLET_SERIAL shell top -b -n 31 -d 2 -p TABLET_PID
adb -s TABLET_SERIAL shell dumpsys battery
```

Repeat status, process identity and memory checks at the end. Reject a
steady-state trial if source identity, workload, transport, encoder PID,
thermal state, display mode, or restart counters change. Do not silently
discard the rejection: preserve its evidence separately.

The raw first trial is deliberately retained despite being unsuitable. A
future harness must discover process replacements automatically, label gaps
and process generations, and fail incomplete trials rather than silently
sampling a dead PID.

## 4. Required baseline experiment before product changes

The full specification is in [the design](../wifi-design.md), sections 3 and
13. It requires an approved instrumentation-only preparation step:

1. Preserve/hash the current installed artifacts, identify their provenance,
   then establish a buildable, approved baseline without overwriting the
   known-good installation. A separate benchmark package/host tool must not
   replace the running tablet application.
2. Build the external workload/measurement harness, including optical
   timing, unique source-frame identifiers, controlled input response,
   complete interval accounting and uncertainty reporting.
3. Capture matched, randomized, replicated runs at fixed display mode,
   brightness, workload, battery/charger state, thermal state and host load.
   Include profiler-off control trials to quantify observation overhead.
4. Record every requested USB metric, confidence intervals and raw evidence.
   Missing metrics, unexplained restarts, or insufficient sensitivity mean
   **BLOCKED**, never PASS.
5. Compare candidate USB with Wi-Fi disabled and with Wi-Fi enabled but USB
   owning the session. Repeat after every approved phase on each host
   platform, not just this Linux machine.

No benchmark implementation, crypto probe APK, protocol modification,
application install, forced lifecycle test, screen recording, network
impairment, or active input injection was performed in Phase 0.

## 5. Before/after ledger

Implementation authorization and subsequent measurement-tool results are
recorded [separately](usb-preparation-results-2026-10-22.md). The Phase 0
observations above remain unchanged.

| Stage | Full USB baseline | Physical latency/input/power | Non-regression verdict |
| --- | --- | --- | --- |
| Phase 0 discovery | Not established; observations above only | Not measured | BLOCKED |
| Instrumentation preparation | External resource collector implemented; long trial rejected | Physical latency/input/power still pending | BLOCKED |
| Phase 1 transport/LAN | Not started | Not measured | NOT RUN |
| Phase 2 direct/adaptation | Not started | Not measured | NOT RUN |
| Phase 3 hardening | Not started | Not measured | NOT RUN |

This ledger must be filled from actual runs, not copied historical numbers
from a different tablet, transport, build, or display mode.
