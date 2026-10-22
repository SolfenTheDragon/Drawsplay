# USB baseline preparation collector

**Scope revision (2026-10-22):** the owner removed camera measurements and
required physical interaction from the implementation gate. Those optional
metrics remain unmeasured. Schema version 2 separates
`optional_unmeasured_physical_metrics` from required software evidence
and adds descriptive resource summaries. The collector still emits
`acceptance: BLOCKED`: resource acquisition alone is not a validated
software non-regression comparison. Its output must not be relabeled as
PASS, nor should historical missing-camera lists be used to demand
equipment the owner has explicitly waived. Repeatable software checks,
stable sessions and artifact provenance remain required.

This **external, read-only Linux tool** records host process-tree CPU/RSS,
tablet CPU/PSS/RSS/swap, raw battery readings, installed binary/APK hashes,
and selected session counters. It does not install an app, modify either
transport, change settings, restart sessions, inject input or capture screen
contents. It needs Python 3.9+, Go (for existing binary build metadata),
ADB debugging access and a running AOA session. No Python packages are needed.

**It is not the complete software USB acceptance benchmark.** Automated
frame/input/reconnect checks, a validated power proxy, controlled workloads,
replicated comparisons and observation-overhead controls remain separate
requirements. Optical/physical checks are optional. A resource observation reports
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
- `summary.json`: software-only scope, observation validity, required missing
  evidence, optional unmeasured physical metrics, and resource distributions.
  CPU summaries omit unknown first samples and separate PID generations.
  Decode snapshot presence is reported separately from still-required
  complete report coverage; observing a value does not satisfy that gate.
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

Only numeric sampled decode delay, presentation-scheduling slack and pending
queue depth are included from the latest stats. These snapshots can be stale
or miss reports; their summaries describe sampled values, not continuous
tails. Frame counts are excluded and never summed into unique FPS. Software
decode delay/slack do not establish photon latency. Battery readings
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

Existing AOA framing, simulated disconnect cleanup and test-only software
input echo measurements can also be reproduced without touching hardware:

```sh
cd host
go test -race -run '^TestUSBBaseline' -count=3 .
go test -run '^$' -bench '^BenchmarkUSBBaseline' -benchmem -benchtime=200ms -count=5 .
```

The echo traverses AOA and mux code over an in-memory pipe, not tablet/OS
input injection. It is a software reference, never measured device RTT.

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s tools/usb_baseline -p 'test_*.py' -v
```

Tests use synthetic procfs/dumpsys records and temporary evidence folders;
they do not contact the tablet, running host or LAN.
