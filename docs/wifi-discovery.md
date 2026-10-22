# Host discovers tablet: development diagnostic

The independent host command browses the approved tablet service type,
`_drawsplay-tablet._tcp.local.`, on an explicitly selected LAN interface.
It is not called by the normal host startup path and adds no USB runtime
work. The old product scaffold still advertises hosts; this diagnostic is
the new direction's discovery component, not a completed replacement.

From `host/`:

```sh
go run ./cmd/wifi-discover -interface YOUR_LAN_INTERFACE -duration 15s
```

The interface must be up, multicast-capable, non-loopback and non-point-to-
point, with a usable IPv4 prefix. There is deliberately no automatic
all-interface fallback. Choose your physical Wi-Fi/Ethernet LAN, not a VPN,
Docker bridge or other virtual interface. Name-based virtual-interface
detection is not reliable; selection is explicit in this tool.

The query is mDNS on the selected link; no public DNS, cloud endpoint or
internet service is consulted. Advertised hostnames and certificate pins
are not used. Only numeric addresses inside the selected interface's
on-link IPv4 prefixes survive candidate validation. Multicast, loopback,
network/broadcast and the host's own addresses are rejected, as are
duplicate TXT keys, old protocol versions, invalid/distinct-port
violations, expired records and excessive metadata. Off-link private
addresses are no more trusted than public ones.

Each stdout line is an **untrusted candidate**, not a paired identity or
authorization to dial/inject/capture. Discovery can be spoofed by LAN peers.
No capture, input injection or trust-store change occurs in this command.
Names may use DNS escaping; they are display labels, never identity keys.
Consumers must not treat a prior candidate as valid after a network change.
Revalidate the selected interface and route before any future connection.

Duration is limited to 1–60 seconds; output keeps at most 64 unique
candidates, and only the first five rejection details are logged. Exit 0
means at least one candidate, 2 means none found, 1 means failure. When using
`go run`, Go reports the tool's nonzero status as its own failure.

The independent mDNS receiver validates the incoming interface, source
port, on-link responder address and required link-local TTL of 255.
DNS datagrams are limited to 4096 bytes and 64 records/questions before
decoding. Its cache has at most 64 services, 64 hostnames and eight
addresses per hostname, with lifetimes capped at 120 seconds and TTL-zero
goodbye handling. Split SRV/TXT/A responses can be assembled without an
unbounded packet queue. Cache capacity exhaustion aborts with an explicit
error. Queries repeat every three seconds until the bounded window ends.

No candidates does not diagnose a firewall or client isolation. Check
tablet availability mode, selected network, multicast filtering and
guest-network policies. The command intentionally says these are possible
causes rather than claiming a timeout identifies which one.

## Verification

```sh
go test -race ./internal/wifilan ./cmd/wifi-discover
go test ./internal/wifilan -run '^$' -fuzz '^FuzzTabletCandidate$' \
  -fuzztime=10s -parallel=2
go vet ./internal/wifilan ./cmd/wifi-discover
```

The isolated [Android NSD probe](../tools/android_wifi_probe/README.md)
advertised from the attached Tab A8 on API 34; the Linux host discovered
it on its selected Wi-Fi interface with the correct tablet address and both
bound ports. The first 15-second run found one candidate and rejected none.
The probe was removed afterwards, leaving the installed DrawSplay package
untouched. No video/input stream was sent.
The final bounded receiver was then tested in a second 10-second run:
one candidate, zero rejected packets, correct tablet address and both ports.
The tablet logged automatic NSD unregistration, socket closure and multicast
lock release after its 40-second lifetime; the separate APK was uninstalled.
Two 10-second fuzz runs exercised candidate metadata (415,781 cases) and
DNS packet parsing (491,140 cases) without failure.

## Remaining release gates

IPv6-only discovery, API 26–32 network-scoped advertisement fallback,
UDP discovery fallback, pairing, route-bound dialing, continuous inventory
removal notifications, reconnect and UI integration remain unimplemented.
The command does not use the existing zeroconf resolver: its internal
unbounded cache and first-result probing policy are unsuitable here.
The existing DNS codec parses bounded datagrams; hostile-LAN floods can
still consume CPU, so bounded parsing is not a denial-of-service immunity
claim. Windows compilation passed, but multicast control-message support
and discovery have only been executed on Linux.
