# Tab A8 software reference and offline crypto probe

Date: 2026-10-22. Camera checks and mandatory physical interaction were
waived by the owner. Measurements below are software observations, not
optical latency, physical input timing or a USB non-regression comparison.

The connected SM-X200 (Android 14/API 34) is the authorized testing device.
The installed DrawSplay app and running host were not replaced or restarted.
No product transport implementation is included in this measurement step.

## Stable USB resource observations

Two five-minute observations, each following a 30 s wait, completed with
150 samples, no acquisition errors and no observed process/session changes
within each window. Thirty seconds is a diagnostic wait, not the original
five-minute thermal warm-up. Desktop content and thermal conditions were
not controlled. Do not treat intervals as independent statistical trials.

| Metric | First window | Decoder-proxy window |
| --- | --- | --- |
| First sample UTC | 15:44:27 | 15:52:28 |
| Last sample UTC | 15:49:25 | 15:57:26 |
| Host service CPU, mean one-core % | 4.416 | 4.782 |
| Active encoder process CPU, mean one-core % | 10.903 | 11.101 |
| Tablet app CPU, mean one-core % | 48.306 | 48.082 |
| Tablet PSS first -> last, KiB | 55,652 -> 53,811 | 54,897 -> 53,413 |
| Tablet RSS first -> last, KiB | 103,188 -> 101,348 | 102,192 -> 100,744 |
| Sampled average-decode-delay mean | Not collected | 26.707 ms |
| Sampled pending queue depth | Not collected | 0 at all 150 samples |

CPU summaries have 149 intervals; initial unknown CPU samples are excluded,
not replaced with zero. Memory has 10 snapshots per window. An idle/zombie
host child is recorded separately; its zero CPU is not encoder CPU.

Raw first-window [metadata](usb-software-scope-2026-10-22/metadata.json),
[samples](usb-software-scope-2026-10-22/samples.jsonl),
[summary](usb-software-scope-2026-10-22/summary.json).
Decoder-window [metadata](usb-decoder-proxies-2026-10-22/metadata.json),
[samples](usb-decoder-proxies-2026-10-22/samples.jsonl),
[summary](usb-decoder-proxies-2026-10-22/summary.json).

The decoder-window summary was produced before the missing-evidence label
was split into measured decode proxies versus still-missing frame delivery
proxies. Its raw output is unchanged. Decode values are samples of the latest
250 ms averages, not all frames, continuous percentiles or source-to-photon
latency. Queue depth excludes internal codec backlog. Missing presentation
slack is unknown, not zero. Frame counters are deliberately not summed.

After review, a final eight-second device smoke run verified that decode
snapshot presence does not silently satisfy report-coverage requirements:
[metadata](usb-scope-smoke-2026-10-22/metadata.json),
[samples](usb-scope-smoke-2026-10-22/samples.jsonl),
[summary](usb-scope-smoke-2026-10-22/summary.json).
It had four samples with no acquisition errors; acceptance remained blocked.

Counters changed between the windows (video connections 10 -> 11 and
capture restarts 12 -> 16). This is not evidence of an uninterrupted session
across both windows, nor a clean matched before/after experiment. No cause
is assigned to between-window changes.

Both windows were USB charging. Raw charge-counter values rose and battery
level increased. These vendor fields do not isolate app power or measure
consumption under fixed load. No watts or power-regression claim is made.

## Host in-memory framing reference

[`host/usb_baseline_test.go`](../../host/usb_baseline_test.go) benchmarks the
unchanged mux read/write and AOA reader. Reproduce from `host/`:

```sh
go test -run '^$' -bench '^BenchmarkUSBBaseline' -benchmem -benchtime=200ms -count=5 .
```

Raw [output](usb-framing-benchmark-2026-10-22.txt). Median of five runs,
Linux amd64, Intel Core i7-10610U:

| Operation / payload bytes | ns/op | B/op | allocs/op |
| --- | --- | --- | --- |
| Mux read / 128 | 92.66 | 144 | 2 |
| Mux read / 512 | 159.8 | 528 | 2 |
| Mux read / 16000 | 2731 | 16400 | 2 |
| Mux write / 128 | 26.89 | 16 | 1 |
| Mux write / 512 | 26.84 | 16 | 1 |
| Mux write / 16000 | 28.78 | 16 | 1 |
| AOA read / 128 | 105.8 | 133 | 2 |
| AOA read / 512 | 152.2 | 517 | 2 |
| AOA read / 16000 | 2536 | 16389 | 2 |

These use memory/discard writers, not USB hardware. Discard-write throughput
is not useful bus throughput. Existing allocations are recorded, not
optimized or falsely described as allocation-free. This reference is for
future comparison with the exact same benchmark/toolchain/host settings.

## On-device offline crypto feasibility

[`tools/android_crypto_probe/`](../../tools/android_crypto_probe/README.md)
was compiled for Java 8 and dexed with minimum API 26 using installed SDK
tools. Executed via Android `app_process` in a separate shell process, no
APK install, no sockets, no DrawSplay settings/source changes. The named
temporary dex was removed after execution. This CPU-loading probe ran
after USB resource collection, not during it.

AES-GCM known-answer vector, decryption, tamper rejection and nonce checks
passed on the physical tablet. Platform provider: `AndroidOpenSSL`; both
AES-GCM and ChaCha20-Poly1305 transformations were available. Raw
[JSONL results](tab-a8-crypto-probe-2026-10-22.jsonl).

One approximately one-second trial per size/algorithm, after 64 warm-up
pairs; wall time per encrypt/decrypt/verify pair:

| Payload | AES-128-GCM | ChaCha20-Poly1305 |
| --- | --- | --- |
| 1 KiB | 35.473 us | 36.183 us |
| 16 KiB | 190.663 us | 177.550 us |
| 64 KiB | 322.885 us | 620.622 us |

These include cipher initialization, provider allocations, byte verification
and scheduling overhead. Payload buffers are reused; the probe does not
meet a product-wide allocation-free promise. Order, JIT, active USB workload
and thermals were not controlled or randomized. Results do not justify
declaring a sustained cipher winner, app CPU budget or TLS overhead.

TLS 1.2/1.3 appeared in supported protocols; older TLS versions also appear
in provider capability output but must never be enabled by the product.
No TLS handshake, network transport, pinned identity, Keystore or mTLS was
exercised. A bounded actual TLS experiment is still necessary.

## Remaining software gate

Physical camera/input checks are optional and no longer block. Required
software evidence still missing: controlled reproducible workload, complete
delivery/drop counters, automated input timing/reconnect checks, validated
power proxy, matched replicated trials/observer-overhead controls and exact
installed source provenance. The running host is a dirty build of an older
revision; the installed APK's source revision remains unknown (hashes are
in metadata). Other Android/host versions remain untested.

These successful observations replace neither the rejected historical
trial nor the remaining comparison work. No product Wi-Fi path has been
enabled and no USB performance equivalence claim is made.
