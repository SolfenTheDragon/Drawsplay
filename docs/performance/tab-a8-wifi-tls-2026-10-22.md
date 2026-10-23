# Real Tab A8 Wi-Fi TLS experiment — 2026-10-22

## What ran

The attached Samsung SM-X200, Android 14/API 34, executed the newly compiled
production `TlsSocketFactory` in an isolated Android shell process.
The production Kotlin classes and Kotlin 1.9.24 standard library were dexed
using d8 with `--min-api 26`. A small Java 8 launcher performed bounded,
synthetic TLS echo exchanges. No APK was installed or replaced.

Both endpoints were on the same IPv4 /24. The host route to the tablet used
its Wi-Fi interface directly, without a gateway or VPN. The host bound its
selected LAN address; the reverse experiment bound the tablet's Wi-Fi
address. Neither listener used a wildcard address. Peer addresses were
checked against the selected opposite device. Literal addresses avoided
DNS; no public endpoint, cloud service or internet traffic was needed.

ADB copied/started the temporary probe and supplied its throwaway pin; it
did not carry the echo payload. The existing DrawSplay USB session was not
reconfigured, benchmarked or replaced. Its background activity is a
confounder, so these are Wi-Fi feasibility observations, not isolated app
performance or USB comparisons.

The host peer used Python/OpenSSL with a temporary P-256 certificate.
The first connection intentionally used the wrong pin and was rejected.
The second verified the pin before any application payload. Each successful
connection sent 10 warm-up plus 100 timed 256-byte echo roundtrips, then
256 stop-and-wait 16 KiB echoes (4 MiB each direction), validating every
returned byte. Both host runs confirmed exactly 4,222,464 echoed bytes,
including the small messages. Owned processes exited and temporary remote
dex files were removed.

## Results

[Raw observations](tab-a8-wifi-tls-2026-10-22.jsonl) are ordered as
tablet-initiated, then host-initiated; each has a wrong-pin rejection record.

| Observation | Tablet initiates TCP | Host initiates TCP |
| --- | ---: | ---: |
| TLS server identity | Host | Host |
| TLS protocol/cipher | TLS 1.3 / AES-256-GCM | TLS 1.3 / AES-256-GCM |
| Correct-pin setup | 24.522 ms | 30.435 ms |
| 256-byte echo median | 6.921 ms | 7.180 ms |
| 256-byte echo p95 | 11.131 ms | 11.733 ms |
| 256-byte echo maximum | 218.531 ms | 14.750 ms |
| 16 KiB stop-and-wait useful payload rate | 10.070 Mbit/s | 6.330 Mbit/s |
| Wrong pin rejected | Yes | Yes |
| TCP_NODELAY enabled | Yes | Yes |

Setup includes local TLS context construction, connection/accept and
handshake; it is not pure cryptographic overhead. Reverse setup includes
time waiting for the host to connect. Rate counts payload in one direction
divided by the full echo elapsed time; it is not link capacity, one-way video
throughput or a maximum sustainable encoding bitrate. The sequential echo
workload deliberately waits for each reply and is not a proposed video
protocol. One trial per direction is insufficient for a performance claim.
The 218.5 ms outlier is retained, not discarded.

## Implementation consequence

`TlsSocketFactory.authenticateAccepted` now supports the approved direction:
host initiates TCP to the tablet, while remaining the TLS server whose
identity the tablet pins. The tablet is the TLS client on the accepted
socket. This role inversion worked on the actual Tab A8, not just the JVM.
The helper takes ownership of its accepted socket, authenticates before
returning, and closes it on invalid policy, wrong pin or handshake failure.
Timeouts bound read inactivity, not total wall-clock handshake duration.

This is a connection primitive, not an integrated discovery/pairing/session
implementation. It does not open a product listener by itself. The temporary
test pin was provisioned over trusted ADB, not learned from an unauthenticated
advertisement or treated as completed numeric-code pairing.

## Limits

No screen pixels or input-injection packets were sent. No video decoder,
discovery, PAKE pairing, mTLS device identity, reconnect, network impairment,
power or long soak was validated. Only this API 34 device and Linux host
were exercised; API 26 compilation is not Android 8 runtime verification.
Binding and routing were checked for this experiment, not enforced as a
general LAN-only product policy by this TLS helper.
