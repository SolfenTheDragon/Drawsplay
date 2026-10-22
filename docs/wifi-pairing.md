# Wi-Fi enrollment and durable identity components

These are **development components, not an enabled product pairing flow**.
The host/tablet enrollment UI, network exchange, local confirmation and
session integration are still required. No screen or input capability is
granted by calling a codec or passing a pairing unit test.

## Numeric-code exchange

The candidate implementation uses SRP-6a with the RFC 5054 3072-bit group,
SHA-256 and standard padded `k`/`u`. The host displays an eight-digit
cryptographically random code, preserving leading zeroes; the tablet user
enters it. A locally opened invitation expires after 120 seconds, allows
five exchanges globally, and permits only one active exchange. Failed,
malformed and aborted challenges consume attempts. Opening a late challenge
does not extend the invitation. Success prevents further exchanges.

The pinned dependencies are:

- Host: [`1Password/srp`](https://github.com/1Password/srp) revision
  `16c52f9109aea2f122bbfe82246f0edb5bcbb92a`, Apache-2.0. The latest
  tag, v0.2.0, lacks the standard-padding constructors; a pinned upstream
  pseudo-version is necessary.
- Android: Bouncy Castle `bcprov-jdk18on:1.86`, using its lightweight SRP
  classes directly. No process-global provider is registered.

SRP arithmetic is performed by these libraries, not copied or reimplemented
in DrawSplay. Their provenance is not a claim that this application protocol
has received an independent cryptographic audit.

For interoperability, `x = SHA256(salt || SHA256("DrawSplay-WiFi-v2:" || code))`.
The Go library's session key is `SHA256(lowercase-unpadded-ASCII-hex(S))`;
the Kotlin adapter deliberately matches this, **not** Bouncy Castle's
different padded-secret key derivation. Do not label this whole exchange
RFC-5054 wire compatible.

The fixed transcript is:

```text
ASCII("DrawSplay numeric pairing\0") || uint32be(2) ||
host-SPKI-SHA256[32] || tablet-SPKI-SHA256[32] ||
nonce[32] || salt[16] || PAD(A)[384] || PAD(B)[384]
```

Both parties enforce exact field lengths, nonzero identity pins and
`1 < public < N`. Tablet confirmation is
`HMAC-SHA256(K, ASCII("tablet confirms\0") || transcript)`; the host checks
it before returning
`HMAC-SHA256(K, ASCII("host confirms\0") || transcript)`.
**Never return the host proof first:** an active peer could otherwise test
code guesses offline. Proof checks are constant-time, role separated and
single-use. No code, verifier, shared secret or proof belongs in logs.

The caller must bind the SPKI values to the actual endpoint identities,
obtain explicit host confirmation, complete pinned mutual TLS and persist
trust only after that succeeds. The component itself does not implement
these caller obligations. Managed `BigInteger`/Go `big.Int` state cannot be
claimed securely erased; byte buffers are cleared where practical.

## Subsequent authenticated sessions

`PairedServerTLS` and `WiFiMutualTls` keep the host as TLS server even when
the host initiates TCP to the tablet. The tablet supplies its own identity
certificate and pins the host's SPKI; the host pins the tablet's SPKI.
Self-signed certificates are not accepted merely because their name or
advertised address matches.

Policies require P-256 signing keys, valid certificate dates and compatible
key usages, TLS 1.2 ECDHE-ECDSA/AES-GCM or TLS 1.3. Missing, changed and
unpaired client identities fail before an authenticated application channel
is admitted. Host session tickets are disabled. Revocation callers must
close active channels and discard their pinned TLS configuration; the
storage operation alone is not active-session revocation.

The new Kotlin mutual-TLS path has an absolute handshake deadline, including
against a slow-dribble peer, and closes failed accepted sockets. This does
not change the older `TlsSocketFactory`'s inactivity-timeout semantics.

## Durable storage

The Go `TrustStore` owns a dedicated private directory and an exclusive
OS-managed file lock. A bounded, versioned 16-KiB record contains the host
identity and at most one paired tablet. Saves use a synced temporary file
and replacement; Linux also syncs the directory. Concurrent writers,
corrupt/oversized records, unsupported versions and expired identities fail
explicitly, without silently generating replacement identities.
A missing identity record in a previously initialized store also fails
explicitly; the retained lock file distinguishes it from first-time setup.

Linux requires current-user ownership, private directory/file permissions
and regular non-symlink files. The private key is stored under those file
permissions. Windows uses current-user DPAPI and a protected user/SYSTEM
DACL; run without elevation. Windows source and tests cross-compile but
DPAPI/ACL runtime behavior has **not** been exercised on Windows hardware.
Windows file replacement does not claim Linux-style directory-fsync
power-loss guarantees.

Android `WiFiTrustStore` creates a P-256 signing identity and a separate
AES-256-GCM trust key in Android Keystore. Both are non-exportable.
The host pin/name is stored as a bounded authenticated record in
`noBackupFilesDir/wifi-v2/trust.bin`, with a fresh provider-generated nonce
and atomic-file replacement followed by readback verification.
There is no plaintext preferences fallback, no backup migration and no
StrongBox/hardware-backing assumption. Missing keys or invalid ciphertext
require explicit local repair/re-pairing, not automatic trust replacement.

## Reproduce

```sh
# From host/
go test -race ./internal/wifilan ./cmd/wifi-pairing-test-peer
go build -o /absolute/session/path/wifi-pairing-test-peer ./cmd/wifi-pairing-test-peer

# From android/
DRAWSPLAY_PAIRING_TEST_PEER=/absolute/session/path/wifi-pairing-test-peer \
  ./gradlew :app:testReleaseUnitTest \
  --tests 'com.drawsplay.tablet.transport.WiFiPairing*Test' \
  --tests 'com.drawsplay.tablet.transport.WiFiMutualTlsTest' \
  --tests 'com.drawsplay.tablet.transport.WiFiTrustRecordCodecTest'
./gradlew :app:assembleWifiTest
```

The stdio peer has a public fixed test code and synthetic identities; it
never listens or saves trust. The cross-language test is explicitly skipped
when that executable is not configured; CI builds/configures it. A configured
missing/broken peer fails the test.

`wifiTest` has the distinct application ID `com.drawsplay.tablet.wifitest`.
Its manifest removes both USB attachment activities; the installed display
application is not replaced. Install only if that test package is absent:

```sh
adb install --no-streaming app/build/outputs/apk/wifiTest/app-wifiTest.apk
adb shell am start -W \
  -n com.drawsplay.tablet.wifitest/com.drawsplay.tablet.WiFiSelfTestActivity
adb logcat -d -s DS-WiFiSelfTest:I '*:S'
adb uninstall com.drawsplay.tablet.wifitest
```

The real Tab A8/API 34 completed identity persistence, encrypted trust
reload, modified-ciphertext rejection without resetting the identity,
explicit trust removal, and non-exportable-key checks. The isolated APK was
uninstalled afterwards. This is Keystore/storage evidence, **not** radio
pairing, hardware-backed-key attestation, Android 8 runtime coverage,
streaming, power or soak evidence. Starting the test activity can background
the display app; do not interpret that as a USB measurement.
The recorded result is in
[`performance/tab-a8-wifi-trust-2026-10-22.json`](performance/tab-a8-wifi-trust-2026-10-22.json).
