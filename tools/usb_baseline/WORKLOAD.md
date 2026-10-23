# Controlled local X11 workload

`workload.py` places a bounded synthetic FFmpeg `testsrc2` animation on an
existing, non-overlapping `Virtual-*` X11 output, then runs the external
collector with its scene identifier. It does not alter monitor configuration,
USB protocol, installed apps, input injection or host privacy settings.
The connected tablet displays the same existing USB stream.

Requirements: the existing extended AOA session, interactive X11 session,
installed `xrandr`/`ffplay`, collector prerequisites, and a non-primary virtual
output. This helper is intentionally not a Wayland/Windows implementation.
Do not install packages just to run it without reviewing the prerequisites.

```sh
python3 tools/usb_baseline/workload.py \
  --host-pid HOST_PID --serial TABLET_SERIAL \
  --virtual-output Virtual-1-1 \
  --output docs/performance/usb-controlled-UNIQUE_NAME
```

Defaults: 1200x800 only if that is the selected output's actual geometry,
60 source fps, 300 s warm-up plus 300 s collection. Dimensions come from
the active virtual output, not a monitor resize. Override duration/warm-up
for a smoke test; do not compare a short smoke to a long run.

The only media source is local lavfi: no downloaded video, URL, DNS or
internet. It requests a borderless window at the virtual output coordinates,
not full-screen on the user's primary display. It refuses primary/physical
outputs, overlap with another active output, absent primary identification,
missing player or non-X11 sessions. It terminates only the player process
it created. User focus can move to this temporary window; do not use it
while actively working on the tablet.

`workload.json` records the scene version, requested geometry, player
version and player-exit error, if any. Early player failure aborts acquisition;
player exit during collection invalidates the resulting resource window.
The scene is stopped on normal exit or handled failure. A hard kill of the
wrapper cannot guarantee cleanup, although the player also has a finite
duration.

Limitations: the window manager can ignore requested placement, the player
can miss source deadlines under load, and this tool does not prove every
source frame appeared at the tablet. Window position must be verified before
using this as final controlled-scene evidence; requested source FPS is not
measured delivery FPS. The extra ffplay process/GPU activity is workload
cost, not host-service CPU; keep its version/settings constant for paired
comparisons. Brightness, thermals, CPU/GPU clock history, charging state and
outside activity still need matching/recording. Privacy blackout remains
authoritative; the helper never unlocks the desktop or suppresses it.

The collector still reports `acceptance: BLOCKED`; a controlled source alone
does not supply complete report accounting, input/power coverage, repeated
matched trials or equivalence sensitivity. Camera and physical interaction
are not required by this helper.
