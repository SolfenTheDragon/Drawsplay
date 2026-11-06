# Contributing to DrawSplay

Thanks for your interest in DrawSplay. Contributions are welcome, with one important note:

**Merging is at the sole discretion of the repository owner.** Opening an issue or pull request does not guarantee it will be reviewed, accepted or merged, and changes may be declined or reworked for any reason, including scope, maintenance cost or project direction. Please don't take it personally.

## Before you start

- For anything beyond a small fix, open an issue first to discuss the idea so you don't spend time on something that won't be merged.
- Report security problems privately, as described in [SECURITY.md](SECURITY.md), not in public issues.

## Pull requests

- Keep each pull request small and focused on one change.
- Describe what changed and why, and how you tested it (OS, GPU and tablet if relevant).
- Make sure CI passes. Run what applies to your change before opening the pull request:
  - `host/`: `gofmt -l .` (no output), `go vet ./...`, `go test -race ./...` and `go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...`. For Windows code, also `GOOS=windows go vet ./...`.
  - `desktop/`: `cargo fmt --check`, `cargo clippy --all-targets -- -D warnings` and `cargo test`.
  - `android/`: `./gradlew :app:compileReleaseKotlin` and `./gradlew :app:testReleaseUnitTest`.
  - `native/drawsplay-media/`: `make CC="cc -Werror"`.
- Pin any new GitHub Action to a full commit SHA with a version comment, like the existing ones.
- Keep Windows and Linux builds both compiling. Platform-specific host code uses `_windows.go`, `_linux.go` and `_other.go` filename suffixes.
- Input and video paths are latency-sensitive: avoid per-event handshakes, unbounded queues and socket I/O on the Android main thread.
- Protocol changes must be additive. Remove a field or framing only once no released tablet app uses it (see `FUNCTIONALITY.md` §7). Don't commit secrets or signing keys.
- The Go host (`host/`) and DrawSplay Desktop's `dd-host` (`desktop/host/`) solve the same problems separately. When you fix one, check the other:
  - X11 virtual output and CVT modes: `host/x11_extend_linux.go` and `desktop/host/src/linux_display.rs`
  - sway/Hyprland outputs: `host/sway_extend_linux.go`, `host/hypr_extend_linux.go` and `desktop/host/src/wayland_display.rs`
  - Windows virtual display: `host/idd_*.go`, `host/display_windows.go` and `desktop/host/src/vdisplay.rs`
  - ffmpeg encoder choice and arguments: `host/ffmpeg.go`, `host/pipeline*.go` and `desktop/host/src/encoder.rs`, `capture.rs`
  - bitrate ladder: `host/ratecontrol.go` and `desktop/host/src/ratecontrol.rs`
  - input injection and key maps: `host/inject*.go`, `host/keymap.go` and `desktop/host/src/inject/`, `desktop/client/src/keys.rs`
  - files both read or write (tested against `testdata/protocols/`): display claims (`host/display_claims_linux.go`, `desktop/host/src/display_claims.rs`), dd-host's `status.json` (`desktop/host/src/status.rs`, `host/desktop_host_status.go`), the driver interface (`host/idd_protocol.go`, `desktop/host/src/idd_protocol.rs`)
- Formats implemented in more than one place have shared test vectors in `testdata/protocols/`. Change the vectors with the format, and run every implementation's tests.
- Update `README.md` if setup or usage changes, `FUNCTIONALITY.md` if behaviour changes, and `PRIVACY.md` if what is stored or sent changes.

## Licensing

DrawSplay is licensed under the GPL-3.0 (see [LICENSE](LICENSE)). By submitting a contribution you agree that it is licensed under the same terms. The bundled Windows driver keeps its MS-PL terms (see [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md)).
