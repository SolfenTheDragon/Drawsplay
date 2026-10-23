#!/usr/bin/env python3
"""External, read-only USB resource observation; never a full acceptance PASS."""

import argparse
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
import hashlib
import http.client
import json
import math
import os
from pathlib import Path
import re
import statistics
import subprocess
import sys
import time


@dataclass(frozen=True)
class ProcessStat:
    pid: int
    ppid: int
    start_ticks: int
    cpu_ticks: int
    rss_pages: int


def parse_stat(text):
    try:
        end = text.rindex(")")
        pid = int(text[:text.index("(")].strip())
        fields = text[end + 1:].split()
        result = ProcessStat(pid, int(fields[1]), int(fields[19]),
                             int(fields[11]) + int(fields[12]), int(fields[21]))
        if min(result.pid, result.start_ticks) <= 0 or min(result.cpu_ticks, result.rss_pages) < 0:
            raise ValueError("invalid process counters")
        return result
    except (ValueError, IndexError) as exc:
        raise ValueError("malformed or truncated proc stat") from exc


def cpu_percent(previous, current, hz):
    before_time, before = previous
    after_time, after = current
    if (before.pid, before.start_ticks) != (after.pid, after.start_ticks):
        raise ValueError("process generation changed")
    elapsed = after_time - before_time
    ticks = after.cpu_ticks - before.cpu_ticks
    if elapsed <= 0 or hz <= 0 or ticks < 0:
        raise ValueError("invalid elapsed time or CPU counter reset")
    return 100 * ticks / hz / elapsed


def parse_meminfo(text):
    totals = {}
    for label, key in (("PSS", "pss_kib"), ("RSS", "rss_kib"),
                       ("SWAP PSS", "swap_pss_kib")):
        match = re.search(r"\bTOTAL " + label + r":\s*(\d+)", text)
        if not match:
            raise ValueError("meminfo missing TOTAL " + label)
        totals[key] = int(match.group(1))
    return totals


def safe_status(raw):
    fields = {
        "aoa": ("state",),
        "video": ("streaming", "encoder", "bitrate", "displayMode", "captureKind"),
        "metrics": ("inputConnections", "inputRestarts", "videoConnections",
                    "videoRestarts", "captureRestarts"),
        "featureFlags": ("wifi",),
        "clientVideoStats": ("intervalMs", "avgDecodeMs", "avgPresentMs", "queuedFrames"),
    }
    result = {k: raw[k] for k in ("version", "aoaEnabled", "adbTunnelEnabled")
              if k in raw and isinstance(raw[k], (str, bool))}
    for section, keys in fields.items():
        value = raw.get(section)
        if isinstance(value, dict):
            result[section] = {k: value[k] for k in keys if k in value
                               and isinstance(value[k], (str, int, float, bool))
                               and not (isinstance(value[k], float)
                                        and not math.isfinite(value[k]))}
            if section == "clientVideoStats":
                result[section] = {key: number for key, number in result[section].items()
                                   if type(number) in (int, float) and number >= 0}
    return result


def assessment(samples, errors, *, duration=None, interval=None):
    issues = list(errors)
    if len(samples) < 2:
        issues.append("fewer than two samples")
    statuses = [s.get("status", {}) for s in samples]
    if any(s.get("aoa", {}).get("state") != "linked"
           or s.get("video", {}).get("streaming") is not True for s in statuses):
        issues.append("AOA streaming not confirmed throughout")
    invariants = [{key: value for key, value in status.items()
                   if key != "clientVideoStats"} for status in statuses]
    if invariants and any(s != invariants[0] for s in invariants[1:]):
        issues.append("session counters, settings or transport changed")
    if duration is not None and interval is not None:
        stamps = [s.get("elapsed_seconds") for s in samples]
        if (not stamps or any(not isinstance(t, (int, float)) or not math.isfinite(t)
                              for t in stamps)
                or stamps[0] > interval * 1.5
                or stamps[-1] + interval * 1.5 < duration
                or any(not 0 < b - a <= interval * 1.5
                       for a, b in zip(stamps, stamps[1:]))):
            issues.append("sampling coverage gap")
    return {
        "acceptance": "BLOCKED",
        "measurement_scope": "software_only",
        "resource_window_valid": not issues,
        "resource_window_issues": sorted(set(issues)),
        "decode_proxy_observation": {
            "samples_with_decode_value": sum(
                "avgDecodeMs" in status.get("clientVideoStats", {}) for status in statuses),
            "status_samples": len(statuses),
            "interpretation": "Observed snapshots, not validated complete report coverage.",
        },
        "optional_unmeasured_physical_metrics": [
            "end_to_end_latency", "unique_optically_presented_fps_and_drop_rate",
            "physical_input_to_photon_and_pen_fidelity",
        ],
        "missing_acceptance_evidence": [
            "software_decode_proxy_coverage",
            "software_frame_delivery_fps_and_drop_proxies",
            "automated_input_timing_and_reconnect_checks",
            "validated_software_power_proxy",
            "controlled_workload_and_matched_thermal_conditions",
            "exact_baseline_source_provenance", "replicated_matched_trials",
            "approved_statistical_sensitivity", "profiler_off_controls",
            "all_platform_baselines",
        ],
    }


def resource_summary(rows):
    def describe(values):
        if not values:
            return None
        return {"samples": len(values), "mean": statistics.mean(values),
                "median": statistics.median(values), "min": min(values), "max": max(values),
                "first": values[0], "last": values[-1]}

    host_cpu = {}
    tablet_cpu = []
    decoder = {key: [] for key in ("avgDecodeMs", "avgPresentMs", "queuedFrames")}
    memory = {key: [] for key in ("pss_kib", "rss_kib", "swap_pss_kib")}
    for row in rows:
        for process in row.get("host_processes", []):
            value = process.get("cpu_percent_one_core")
            if value is not None:
                key = f"{process['pid']}:{process['start_ticks']}"
                host_cpu.setdefault(key, []).append(value)
        value = row.get("tablet", {}).get("cpu_percent_one_core")
        if value is not None:
            tablet_cpu.append(value)
        for key, values in memory.items():
            value = row.get("tablet_memory", {}).get(key)
            if value is not None:
                values.append(value)
        for key, values in decoder.items():
            value = row.get("status", {}).get("clientVideoStats", {}).get(key)
            if value is not None:
                values.append(value)
    return {
        "cpu_units": "percent of one logical core; intervals are correlated, not independent trials",
        "host_process_cpu": {key: describe(values) for key, values in host_cpu.items()},
        "tablet_cpu": describe(tablet_cpu),
        "tablet_memory": {key: describe(values) for key, values in memory.items()},
        "sampled_decoder_proxies": {key: describe(values) for key, values in decoder.items()},
        "interpretation": "Descriptive resource values only, not a non-regression verdict.",
    }


def positive_seconds(value):
    number = float(value)
    if not math.isfinite(number) or number <= 0:
        raise ValueError("seconds must be finite and positive")
    return number


def command(args, label, timeout=8):
    try:
        result = subprocess.run(args, capture_output=True, text=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired) as exc:
        # Do not include command arguments: they contain the private device serial.
        raise RuntimeError(f"{label}: {type(exc).__name__}") from exc
    if result.returncode:
        raise RuntimeError(f"{label}: command exit {result.returncode}")
    return result.stdout


def read_status():
    # Fixed numeric loopback destination, no proxy, DNS, redirects or config endpoint.
    connection = http.client.HTTPConnection("127.0.0.1", 27180, timeout=5)
    try:
        connection.request("GET", "/api/status")
        response = connection.getresponse()
        body = response.read(1024 * 1024 + 1)
        if response.status != 200 or len(body) > 1024 * 1024:
            raise ValueError("status response unsuccessful or oversized")
        raw = json.loads(body)
        if not isinstance(raw, dict):
            raise ValueError("status response is not an object")
        return safe_status(raw)
    finally:
        connection.close()


def host_tree(root):
    processes = {}
    for entry in Path("/proc").iterdir():
        if not entry.name.isdecimal():
            continue
        try:
            stat = parse_stat((entry / "stat").read_text())
            processes[stat.pid] = stat
        except (FileNotFoundError, ProcessLookupError, PermissionError):
            continue  # Non-owned processes may exit or be inaccessible during enumeration.
    if root not in processes:
        raise ValueError("host PID is no longer readable")
    selected = {root}
    while True:
        children = {p.pid for p in processes.values() if p.ppid in selected}
        expanded = selected | children
        if expanded == selected:
            return [processes[pid] for pid in sorted(selected)]
        selected = expanded


def parse_tablet_process(text):
    lines = text.splitlines()
    if len(lines) != 3:
        raise ValueError("tablet PID/uptime/stat response incomplete")
    pids = lines[0].split()
    if len(pids) != 1:
        raise ValueError("expected one running tablet app process")
    uptime = lines[1].split()
    if not uptime:
        raise ValueError("tablet uptime missing")
    stamp = float(uptime[0])
    stat = parse_stat(lines[2])
    if stat.pid != int(pids[0]) or not math.isfinite(stamp):
        raise ValueError("tablet PID changed while sampling")
    return stamp, stat


def read_tablet(adb):
    return parse_tablet_process(adb(
        'p=$(pidof com.drawsplay.tablet) || exit 1; '
        'echo "$p"; cat /proc/uptime; cat /proc/$p/stat',
        "tablet process counters"))


def battery_values(text):
    result = {}
    for line in text.splitlines():
        key, separator, value = line.strip().partition(":")
        if separator and key in ("USB powered", "AC powered", "Wireless powered",
                                  "level", "voltage", "temperature", "current now",
                                  "Charge counter", "status"):
            result[key] = value.strip()
    if "level" not in result or "temperature" not in result:
        raise ValueError("battery report missing level/temperature")
    return result


def identity(host_pid, adb):
    digest = hashlib.sha256()
    with Path(f"/proc/{host_pid}/exe").open("rb") as binary:
        for block in iter(lambda: binary.read(1024 * 1024), b""):
            digest.update(block)
    root = Path(__file__).resolve().parents[2]
    source = command(["git", "-C", str(root), "rev-parse", "HEAD"], "source revision").strip()
    build = command(["go", "version", "-m", f"/proc/{host_pid}/exe"],
                    "host build metadata")
    revisions = re.findall(r"^\s*build\s+(vcs(?:\.\w+)?=\S+)", build, re.MULTILINE)
    props = {}
    for prop in ("ro.product.model", "ro.build.version.sdk", "ro.board.platform"):
        props[prop] = adb("getprop " + prop, "tablet property").strip()
    # Hash installed split/base APKs without pulling or replacing them.
    paths = adb("pm path com.drawsplay.tablet", "installed APK paths").splitlines()
    hashes = []
    for line in paths:
        if not re.fullmatch(r"package:/data/app/[A-Za-z0-9_/=.+~-]+", line):
            raise ValueError("unexpected installed APK path format")
        value = adb("sha256sum " + line.removeprefix("package:"), "installed APK hash")
        parts = value.split()
        if not parts:
            raise ValueError("installed APK hash missing")
        digest_value = parts[0]
        if not re.fullmatch(r"[0-9a-f]{64}", digest_value):
            raise ValueError("invalid installed APK hash")
        hashes.append(digest_value)
    if not hashes:
        raise ValueError("no installed APKs found")
    return {"source_revision": source, "host_sha256": digest.hexdigest(),
            "host_build_vcs": revisions, "tablet_properties": props,
            "installed_apk_sha256": hashes,
            "host_logical_cpus": os.cpu_count(), "host_clk_tck": os.sysconf("SC_CLK_TCK")}


def collect(args, output):
    def adb(script, label):
        return command(["adb", "-s", args.serial, "shell", script], label)

    errors = []
    statuses = []
    count = 0
    previous_host = {}
    previous_tablet = None
    first_generations = {(p.pid, p.start_ticks) for p in host_tree(args.host_pid)}
    host_hz = os.sysconf("SC_CLK_TCK")
    tablet_hz = int(adb("getconf CLK_TCK", "tablet clock ticks").strip())
    if tablet_hz <= 0:
        raise ValueError("invalid tablet clock ticks")
    metadata = identity(args.host_pid, adb)
    metadata.update({
        "schema_version": 2, "started_utc": datetime.now(timezone.utc).isoformat(),
        "duration_seconds": args.duration, "warmup_seconds": args.warmup,
        "interval_seconds": args.interval, "detail_interval_seconds": args.detail_interval,
        "tablet_clk_tck": tablet_hz,
        "cpu_units": "percent of one logical core; 100 means one core",
        "power_units": "raw vendor battery proxy; not calibrated watts",
        "status_counters": "snapshots only, not summed FPS or unique-frame drops",
        "workload": args.workload,
    })
    (output / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
    time.sleep(args.warmup)
    started = time.monotonic()
    due = started
    next_detail = started
    with (output / "samples.jsonl").open("x") as stream:
        while time.monotonic() - started < args.duration:
            time.sleep(max(0, due - time.monotonic()))
            if time.monotonic() - started >= args.duration:
                break
            row = {"index": count, "elapsed_seconds": time.monotonic() - started,
                   "utc": datetime.now(timezone.utc).isoformat()}
            row_errors = []
            try:
                stamp = time.monotonic()
                processes = host_tree(args.host_pid)
                generations = {(p.pid, p.start_ticks) for p in processes}
                if generations != first_generations:
                    row_errors.append("host process tree generation changed")
                snapshots = []
                for stat in processes:
                    key = (stat.pid, stat.start_ticks)
                    value = asdict(stat)
                    value["rss_kib"] = stat.rss_pages * os.sysconf("SC_PAGE_SIZE") / 1024
                    value["cpu_percent_one_core"] = (
                        cpu_percent(previous_host[key], (stamp, stat), host_hz)
                        if key in previous_host else None)
                    snapshots.append(value)
                previous_host = {(p.pid, p.start_ticks): (stamp, p) for p in processes}
                row["host_processes"] = snapshots
                tablet = read_tablet(adb)
                row["tablet"] = asdict(tablet[1])
                row["tablet"]["cpu_percent_one_core"] = (
                    cpu_percent(previous_tablet, tablet, tablet_hz) if previous_tablet else None)
                row["tablet"]["uptime_seconds"] = tablet[0]
                previous_tablet = tablet
                row["status"] = read_status()
                statuses.append({"status": row["status"],
                                 "elapsed_seconds": row["elapsed_seconds"]})
                if time.monotonic() >= next_detail:
                    row["tablet_memory"] = parse_meminfo(adb(
                        "dumpsys meminfo com.drawsplay.tablet", "tablet memory"))
                    row["tablet_battery_raw"] = battery_values(adb(
                        "dumpsys battery", "tablet battery"))
                    next_detail = time.monotonic() + args.detail_interval
            except (OSError, ValueError, RuntimeError, http.client.HTTPException) as exc:
                row_errors.append(str(exc))
            row["collection_seconds"] = time.monotonic() - (started + row["elapsed_seconds"])
            if row["collection_seconds"] > args.interval:
                row_errors.append("collection overran sampling interval")
            row["errors"] = row_errors
            errors.extend(row_errors)
            stream.write(json.dumps(row, allow_nan=False) + "\n")
            stream.flush()
            count += 1
            due += args.interval
            if due < time.monotonic():
                due = time.monotonic() + args.interval
    summary = assessment(statuses, errors, duration=args.duration, interval=args.interval)
    with (output / "samples.jsonl").open() as recorded:
        summary["resources"] = resource_summary(json.loads(line) for line in recorded)
    summary.update({"sample_count": count, "error_count": len(errors),
                    "elapsed_seconds": time.monotonic() - started,
                    "interpretation": "Software resource observation; workload conditions require "
                                      "separate validation, not an acceptance verdict."})
    (output / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    return summary


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host-pid", type=int, required=True)
    parser.add_argument("--serial", required=True, help="local adb selector; never persisted")
    parser.add_argument("--output", type=Path, required=True, help="new directory; never overwritten")
    parser.add_argument("--duration", type=positive_seconds, default=300)
    parser.add_argument("--warmup", type=float, default=300)
    parser.add_argument("--interval", type=positive_seconds, default=2)
    parser.add_argument("--detail-interval", type=positive_seconds, default=30)
    parser.add_argument("--workload", default="uncontrolled desktop; not acceptance",
                        help="non-sensitive description of the controlled scene, if any")
    args = parser.parse_args(argv)
    if (args.host_pid <= 0 or not math.isfinite(args.warmup) or not 0 <= args.warmup <= 3600
            or args.duration > 86400 or args.interval < 1 or args.detail_interval < args.interval):
        parser.error("require PID > 0, warmup 0..3600, duration <=86400, interval >=1, detail >=interval")
    if not sys.platform.startswith("linux"):
        parser.error("this procfs collector supports Linux hosts only; Windows remains unmeasured")
    created = False
    try:
        args.output.mkdir(mode=0o700, parents=False, exist_ok=False)
        created = True
        summary = collect(args, args.output)
    except (OSError, ValueError, RuntimeError, http.client.HTTPException, KeyboardInterrupt) as exc:
        message = str(exc) or type(exc).__name__
        if created:
            try:
                (args.output / "failure.json").write_text(json.dumps({
                    "acceptance": "BLOCKED", "error": message,
                }, indent=2) + "\n")
            except OSError as write_error:
                print(f"Could not persist failure report: {write_error}", file=sys.stderr)
        print(f"Collection failed: {message}", file=sys.stderr)
        return 1
    print(json.dumps(summary, indent=2))
    # Distinguish collected-but-blocked evidence from success and acquisition failure.
    return 2


if __name__ == "__main__":
    sys.exit(main())
