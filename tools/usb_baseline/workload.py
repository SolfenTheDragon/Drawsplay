#!/usr/bin/env python3
"""Run a synthetic local-only scene on an existing X11 virtual output."""

import argparse
import json
import math
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import time

import collect as collector


def virtual_output(text, name):
    if not re.fullmatch(r"Virtual-[A-Za-z0-9-]+", name):
        raise ValueError("only an explicitly named Virtual-* output is allowed")
    rectangles = {}
    primary = None
    for line in text.splitlines():
        match = re.match(r"(\S+) connected (primary )?(\d+)x(\d+)([+-]\d+)([+-]\d+)", line)
        if match:
            output, is_primary, w, h, x, y = match.groups()
            rectangles[output] = tuple(map(int, (w, h, x, y)))
            if is_primary:
                primary = output
    if name not in rectangles or name == primary or primary is None:
        raise ValueError("require active named virtual output and a different primary")
    width, height, left, top = rectangles[name]
    for output, (w, h, x, y) in rectangles.items():
        if output != name and left < x + w and x < left + width and top < y + h and y < top + height:
            raise ValueError("virtual output overlaps another output; refuse to cover it")
    return width, height, left, top


def player_args(executable, geometry, fps, seconds):
    width, height, x, y = geometry
    if (not 1 <= width <= 4096 or not 1 <= height <= 2160 or not 1 <= fps <= 120
            or not math.isfinite(seconds) or not 1 <= seconds <= 90000):
        raise ValueError("invalid bounded workload dimensions, FPS or duration")
    return [executable, "-hide_banner", "-loglevel", "error", "-nostats",
            "-an", "-sn", "-autoexit", "-noborder", "-window_title",
            "DrawSplay synthetic USB reference", "-left", str(x), "-top", str(y),
            "-x", str(width), "-y", str(height), "-t", str(seconds),
            "-f", "lavfi", "-i", f"testsrc2=size={width}x{height}:rate={fps}"]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host-pid", required=True, type=int)
    parser.add_argument("--serial", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--virtual-output", default="Virtual-1-1")
    parser.add_argument("--fps", type=int, default=60)
    parser.add_argument("--duration", type=collector.positive_seconds, default=300)
    parser.add_argument("--warmup", type=collector.positive_seconds, default=300)
    args = parser.parse_args(argv)
    player = None
    try:
        if args.output.exists():
            raise ValueError("output already exists; refuse to overwrite evidence")
        if os.environ.get("XDG_SESSION_TYPE") != "x11" or not os.environ.get("DISPLAY"):
            raise ValueError("workload requires an interactive X11 session; no Wayland assumption")
        executable = shutil.which("ffplay")
        if executable is None:
            raise ValueError("ffplay missing; install explicitly before running controlled scenes")
        if args.host_pid <= 0 or not 0 < args.warmup <= 3600 or args.duration > 86400:
            raise ValueError("invalid PID, warmup or duration")
        xrandr = collector.command(["xrandr", "--query"], "virtual display query")
        geometry = virtual_output(xrandr, args.virtual_output)
        status = collector.read_status()
        if (status.get("aoa", {}).get("state") != "linked"
                or status.get("video", {}).get("streaming") is not True
                or status.get("video", {}).get("displayMode") != "extend"):
            raise ValueError("an active extended AOA session is required")
        # Do not request input unlock, change privacy policy or restart the session.
        command = player_args(executable, geometry, args.fps, args.duration + args.warmup + 15)
        version = collector.command([executable, "-version"], "workload player version").splitlines()[0]
        player = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                  stderr=None)
        time.sleep(1)
        if player.poll() is not None:
            player.communicate()
            raise ValueError(f"synthetic player stopped before collection "
                             f"(exit {player.returncode}); see player diagnostics above")
        scene = f"lavfi-testsrc2-v1:{geometry[0]}x{geometry[1]}:{args.fps}fps"
        result = collector.main([
            "--host-pid", str(args.host_pid), "--serial", args.serial,
            "--output", str(args.output), "--duration", str(args.duration),
            "--warmup", str(args.warmup), "--workload", scene,
        ])
        workload_error = None
        if player.poll() is not None:
            workload_error = "player exited before collection completed"
            result = 1
        if args.output.is_dir():
            (args.output / "workload.json").write_text(json.dumps({
                "scene": scene, "geometry": geometry, "player_version": version,
                "source": "synthetic local lavfi; no network/media input",
                "limitation": "window placement is requested, not proven; source FPS is requested",
                "error": workload_error,
            }, indent=2) + "\n")
            if workload_error:
                # The resource summary alone cannot know that its source scene died.
                summary_path = args.output / "summary.json"
                if summary_path.exists():
                    summary = json.loads(summary_path.read_text())
                    summary["resource_window_valid"] = False
                    summary["resource_window_issues"].append(workload_error)
                    summary_path.write_text(json.dumps(summary, indent=2) + "\n")
                print(workload_error, file=sys.stderr)
        return result
    except (OSError, ValueError, RuntimeError) as exc:
        print(f"Controlled workload failed: {exc}", file=sys.stderr)
        return 1
    finally:
        if player is not None:
            if player.poll() is None:
                player.terminate()
            try:
                player.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                player.kill()
                player.communicate()


if __name__ == "__main__":
    sys.exit(main())
