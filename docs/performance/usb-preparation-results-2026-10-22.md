# USB measurement preparation: implementation and first run

Date: 2026-10-22. Branch: `Wireless_Implementation`.

**Full USB baseline: BLOCKED. Wi-Fi product implementation: NOT STARTED.**
The approved first implementation step is an external, read-only collector,
not a change to the host/Android applications. Local `main` and the wireless
branch both began at `fe52f8592f4efac60d137dde297c0a1573142a80`;
`git merge --ff-only main` reported already up to date.

## What is implemented

[`tools/usb_baseline/`](../../tools/usb_baseline/README.md) contains a
standard-library Python CLI, unit tests and usage documentation. CI runs
the same tests without hardware. The collector:

- Discovers the host process tree each sample and computes CPU from procfs
  counter deltas with PID/start-time generation checks; no dead-PID zero.
- Uses tablet uptime/CPU counters, low-cadence PSS/RSS/swap and raw battery
  readings through read-only ADB.
- Records host binary/build identity and installed APK hashes without
  replacing/pulling/installing them.
- Stores whitelisted localhost status snapshots; excludes client labels,
  serials, tokens, full command lines, input and screen content.
- Flushes JSONL evidence to a new private directory and preserves explicit
  acquisition errors. Never overwrites evidence. A resource observation,
  including an error-free one, exits 2 with acceptance **BLOCKED**.

Generation baselines include the start of warm-up. Gaps greater than
1.5 sampling intervals and missing start/end coverage invalidate the
resource window. These are sampler validity limits, not performance
regression tolerances.

The tooling test suite has 21 tests. Review fixes cover missed sampling
gaps, warm-up generation changes, malformed ADB output and original
Android randomized APK directories containing `~~`. The workflow passed
the repository's actionlint command. These checks establish tooling
behavior, not product performance or USB non-regression.

## Recorded trial: rejected, evidence retained

Raw files: [metadata](usb-preparation-2026-10-22/metadata.json),
[150 interval samples](usb-preparation-2026-10-22/samples.jsonl),
[blocked summary](usb-preparation-2026-10-22/summary.json).

Five-minute wait/warm-up, then approximately 300 s collection at two-second
intervals, with memory/battery snapshots about every 30 s. Observation:
2026-10-22 14:54:54.620-14:59:52.620 UTC for the first/last samples.
The desktop workload, brightness, thermal history and user activity were
not controlled. No active input or synthetic scene was injected.

The session changed during the window:

| Elapsed | Observed state |
| --- | --- |
| 0 s | AOA linked; h264_vaapi; video connections/restarts 6/5, capture restarts 7 |
| 48 s | Video not streaming; video connections/restarts 7/6 |
| 50 s | Encoder reported `libx264_blackout` |
| 66-194 s | Capture restart counter advanced to 12; process tree changed |
| 206 s | Encoder returned to h264_vaapi; video connections/restarts 8/7 |

AOA remained linked in the snapshots. The host/tablet applications and
transport were not modified or restarted by the collector. The cause of
the blackout/restarts is not assigned: these observations do not distinguish
normal privacy/lifecycle behavior, outside activity, existing faults or
measurement perturbation. No attempt was made to defeat blackout or
change the lock/privacy policy to obtain better numbers.

There were 126 sample-level process-generation-change flags. The correct
verdict is **invalid steady-state window**, not 126 application crashes
and not a failed Wi-Fi comparison. Wi-Fi was disabled throughout.

Descriptive values for the entire **rejected** window, not benchmarks:

| Metric | Observation |
| --- | --- |
| Host service CPU | 149 intervals; mean 9.745%, median 13.00%, range 3.00-15.50% |
| Tablet app CPU | 149 intervals; mean 66.762%, median 80.81%, range 25.50-88.00% |
| Tablet PSS | First 57,114 kB; last 57,085 kB; 10 snapshots |
| Tablet RSS | First 101,132 kB; last 100,980 kB |
| Tablet swap PSS | First 23,206 kB; last 23,078 kB |
| Collection work per sample | Maximum about 370.5 ms, not equivalent to USB latency overhead |
| Battery last snapshot | USB charging, 26%, raw voltage 3810, temperature 280, current-now 252000 |

CPU is one-core percent (100% = one logical core). Raw battery values are
vendor fields, not calibrated power. Encoder CPU must be analyzed by
generation; do not combine blackout and real video into a stable encoder
benchmark. Two memory endpoints do not establish leak/LMK safety.

The long run used the initial collector before the review's warm-up/cadence
validation fixes. It was already rejected by generation/status checks.
Raw files are retained unchanged, not rewritten to claim a final-tool run.

## Final-tool smoke observation

Raw [metadata](usb-preparation-smoke-2026-10-22/metadata.json),
[samples](usb-preparation-smoke-2026-10-22/samples.jsonl),
[summary](usb-preparation-smoke-2026-10-22/summary.json).

An eight-second, zero-warm-up run of the reviewed collector produced four
samples, no acquisition errors, and a valid resource window. Acceptance
remained **BLOCKED**. This confirms the CLI works against this tablet,
not that an eight-second window is a baseline.

Installed base APK SHA-256:
`783b9b65025904e986a10b6f755ef1cee47f8211e90824651b4f9d3e8bc22cf1`.
The host hash remains
`841de7e05058346697034da41981d0ad2ffcc30db15893ae8f64f231ca0ddb19`,
with embedded revision `76837e1eead65c823b34e97198c4eedf49112048`,
`vcs.modified=true`. APK source provenance remains unknown; neither
artifact is proven source-equivalent to the checkout.

## Gate and next required evidence

No product transport changes can proceed on these results. Still required:
a stable, fixed workload and reproducible installed baseline, optical
source-to-photon/unique-frame timing, controlled OS-input responder and
input-to-photon measurements, calibrated power proxy, profiler-off
controls, matched repeated trials with approved sensitivity, and required
host/Android hardware matrix coverage. Crypto overhead is still unmeasured.

Those missing measurements require hardware/physical interaction not
available to this terminal session. Having ADB access does not substitute
for a camera/photodiode, power meter, active-pen digitizer or operator-run
physical lifecycle tests. Do not silently relax the user's gate or present
this partial collector as the completed full benchmark.
