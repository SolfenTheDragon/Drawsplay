# USB baseline preparation collector

**Scope revision (2026-10-22):** the owner removed camera measurements and
required physical interaction from the implementation gate. Those optional
metrics remain unmeasured. This initial collector still always emits
`acceptance: BLOCKED`; it has not yet been extended into the revised
software-only baseline assessment. Its output must not be relabeled as
PASS, nor should its historical missing-camera list be used to demand
equipment the owner has explicitly waived. Repeatable software checks,
stable sessions and artifact provenance remain required.

This **external, read-only Linux tool** records host process-tree CPU/RSS,
tablet CPU/PSS/RSS/swap, raw battery readings, installed binary/APK hashes,
and selected session counters. It does not install an app, modify either
transport, change settings, restart sessions, inject input or capture screen
contents. It needs Python 3.9+, Go (for existing binary build metadata),
ADB debugging access and a running AOA session. No Python packages are needed.

**It is not the complete USB acceptance benchmark.** Optical latency, unique
presented FPS/drops, input RTT/input-to-photon, calibrated power, controlled
workloads, replicated comparisons and observation-overhead controls remain
separate requirements. A successful resource observation always reports
`acceptance: BLOCKED`; it cannot authorize product changes.

## Run

Verify the host PID and select the tablet's ADB serial locally:

```sh
adb devices
ps -eo pid,comm
python3 tools/usb_baseline/collect.py \
  --host-pid HOST_PID --serial TABLET_SERIAL \
  --output docs/performance/usb-run-UNIQUE_NAME
```

The output directory's parent must already exist. The tool refuses to
overwrite any directory and creates evidence directories with mode 0700.
The serial selects the device but is not persisted. Do not put a serial,
private document title or secret in `--workload` or the output path.
The local status request is hardcoded to `127.0.0.1:27180/api/status`: no
external DNS, redirects or proxy are used. Unexpected status fields are
not saved. No screen, input coordinates or complete process command lines
are recorded. Inspect evidence before sharing it.

Defaults: 300 s warm-up, 300 s collection, 2 s CPU/status sampling, and
30 s memory/battery sampling. A diagnostic smoke run can use
`--warmup 0 --duration 8`, but it is not an acceptance trial.
CPU is interval CPU time divided by elapsed time in **one-core percent**;
100% means one logical core, not the whole machine. First samples have no
CPU percentage, rather than a fabricated zero. Host children are discovered
each interval, including capture/helper process generations; CPU samples
are not joined across different PIDs/start times. Tablet timing uses its
own uptime, not host/device wall-clock synchronization.

Outputs:

- `metadata.json`: source revision, running host binary hash/build VCS,
  installed APK hashes, tablet model/API, timing settings and units.
- `samples.jsonl`: flushed interval evidence, process generations, selected
  status, low-cadence memory and raw battery readings, explicit errors.
- `summary.json`: observation validity and missing acceptance evidence.
- `failure.json`: explicit acquisition/interruption failure when collection
  cannot finish; partial files remain for diagnosis.

Exit codes: **1** acquisition/setup failure; **2** evidence recorded but
acceptance remains blocked. There is no acceptance-PASS exit code.
A session/settings/counter change, PID generation change, command failure,
sampling overrun or lost AOA stream invalidates the steady-state window.
Host process generations are recorded before warm-up as well. Gaps greater
than 1.5 sampling intervals, or missing beginning/end coverage beyond that
tolerance, invalidate the resource window. Read the summary even when
files were produced.

The latest video stats are deliberately excluded: they count NALs/codec
outputs and cannot be summed into unique presented FPS. Battery readings
are vendor values, often cached and collected while USB charging; they
are not application watts. `dumpsys meminfo` can disturb the app; compare
against profiler-off controls before drawing performance conclusions.
Gaps/transient children between samples and codec/compositor processes
outside the host tree are not completely captured. The package parser
currently accepts installed APKs under `/data/app/`; adopted/external
storage layouts fail explicitly. Older Android versions may not expose
the required RSS totals/procfs; failure is not a zero-memory result.

Follow [the design's measurement contract](../../docs/wifi-design.md#33-baseline-preparation-not-a-fabricated-phase-0-pass).
Run matched fixed-workload trials and preserve rejected windows. Do not
replace the known-good installed app/host merely to match source revision.
Windows collection, optical/input instrumentation, automated controlled
workloads, calibrated power and statistical equivalence comparison are
**not implemented** by this tool.

## Test

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s tools/usb_baseline -p 'test_*.py' -v
```

Tests use synthetic procfs/dumpsys records and temporary evidence folders;
they do not contact the tablet, running host or LAN.
