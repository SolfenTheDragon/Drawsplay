# Wi-Fi-only major-2 framing component

`host/internal/wifilan/wire.go` and Android `WiFiFrameCodec.kt` implement
the approved fixed big-endian envelope independently of USB.
They are not yet connected to the product's video/input session pumps.
TLS authentication, pairing, capability negotiation and channel association
must succeed before a future caller admits application records.
The codec is **not an authentication boundary by itself**.

| Offset | Field | Size |
| --- | --- | --- |
| 0 | Record type | 1 |
| 1 | Flags, currently zero | 1 |
| 2 | Header length, exactly 36 | 2 |
| 4 | Payload length | 4 |
| 8 | Session epoch | 8 |
| 16 | Sequence | 4 |
| 20 | Sender monotonic capture/event time, microseconds | 8 |
| 28 | Duration, microseconds (video AU only) | 4 |
| 32 | Reserved, zero | 4 |

Types: 1 bootstrap hello, 2 control, 3 input, 4 video attach, 5 config,
6 encoded video access unit. Undefined types/flags/reserved values fail.
Hello requires zero epoch/sequence; all other records require nonzero
epoch/sequence. Duration must be zero outside type 6. Epoch is an opaque
64-bit tag, including values with the high bit set, not an auth token.
Major version 2 must still be negotiated in an authenticated bootstrap
payload; this header layout does not imply negotiation has happened.

Payload ceilings: hello/control/input/attach 4096 bytes, config 65536 bytes,
video AU 1048576 bytes. Empty payloads are invalid. A larger AU must fail
explicitly and require recovery/renegotiation, never an on-demand buffer
growth. These limits reserve room for future bounded slots under the
4 MiB compressed-memory budget; a full slot pool/decoder is not implemented
by this codec.

## Ownership and error handling

Read and validate the header first, then perform session/sequence checks,
then lease a sufficiently sized caller-owned payload slot and read directly
into it. Neither implementation allocates payload storage, queues it or
copies it to another app-owned buffer.

Go reader/writer instances reuse 36-byte arrays. Kotlin additionally reuses
one mutable header object: it is borrowed until the next header read and
must not be retained by an asynchronous consumer. Copying/allocating a
header object per frame is not required. Instances are single-owner, not
concurrent stream multiplexers. Payload storage remains leased until its
consumer is finished; this component never releases a slot.

The sequence guard is separate and must live across video-only reconnects.
Use one guard per direction/channel. Duplicate, decreasing, zero or wrong-
epoch values fail without advancing state. After uint32 maximum, start a
fresh authenticated session instead of wrapping or resetting the counter.

Reading another header before its payload, or a payload without a validated
header, is an explicit error. A too-small destination consumes no payload,
so a caller may select its correct preallocated slot. Malformed/truncated
wire data is a session failure: close the connection, not a resync scan for
potential headers in untrusted payload bytes.

## Verified

Both implementations round-trip the same literal 39-byte fixture
(36-byte input header plus `abc`), reject malformed headers before reading
payload, and cover truncated data, read order and replay/wrap.
Go additionally covers partial and zero-progress writes. The Android JVM
suite exercises 10,000 reproducible mutated/random and truncated records
with a fixed caller-owned payload buffer; invalid records fail with I/O
errors rather than unchecked crashes or on-demand payload allocation.
The Go test observed **0 allocations** per combined frame read/write over
1000 repetitions with preallocated storage. The Kotlin/JDK test likewise
observed **0 additional allocated bytes** over 1000 warmed frame operations,
subtracting calibrated reflection measurement overhead.
These are codec-only measurements, not TLS/MediaCodec/provider allocation
claims or real-device ART profiling.

The real Tab A8 also ran the compiled production Kotlin codec in an
isolated shell process over host-initiated LAN TLS using the production
Go codec on Linux. All nine synthetic AU envelopes and payloads matched,
including 4 KiB, 64 KiB and 1 MiB records, for **3,354,624 verified payload
bytes each direction**. Each endpoint reused one 1 MiB payload buffer.
No screen or OS input data was involved.

```sh
# From host/
go test -race ./internal/wifilan ./cmd/wifi-discover ./cmd/wifi-frame-probe
go test ./internal/wifilan -run '^$' -fuzz '^FuzzWiFiEnvelope$' \
  -fuzztime=10s -parallel=2
# From android/
./gradlew :app:testReleaseUnitTest \
  --tests 'com.drawsplay.tablet.transport.WiFiFrameCodecTest'
```

The initial 10-second envelope fuzz run completed 362,115 cases with no
failure. Physical-tablet reproduction is documented in
[`tools/android_wifi_probe/README.md`](../tools/android_wifi_probe/README.md).
The interoperability experiment does not establish video FPS, decoder
latency, sustained allocation behavior, power or production pairing.
