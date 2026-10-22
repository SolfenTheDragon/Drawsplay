#!/usr/bin/env python3
"""Compare paired USB resource runs; never infer equivalence from non-significance."""

import argparse
import json
import math
from pathlib import Path
import statistics
import sys

import collect as collector


METRICS = {
    "tablet_cpu_one_core_percent": ("tablet_cpu", None),
    "tablet_pss_kib": ("tablet_memory", "pss_kib"),
    "tablet_rss_kib": ("tablet_memory", "rss_kib"),
    "sampled_decode_ms": ("sampled_decoder_proxies", "avgDecodeMs"),
    "sampled_queue_depth": ("sampled_decoder_proxies", "queuedFrames"),
}


def metric_mean(distribution, label):
    if not isinstance(distribution, dict):
        raise ValueError(f"missing metric: {label}")
    mean = distribution.get("mean")
    samples = distribution.get("samples")
    if (type(mean) not in (int, float) or not math.isfinite(mean) or mean < 0
            or type(samples) is not int or samples < 1):
        raise ValueError(f"invalid metric distribution: {label}")
    return mean


def run_metrics(run):
    summary = run["summary"]
    if summary.get("resource_window_valid") is not True or summary.get("error_count") != 0:
        raise ValueError("invalid resource window; rejected trials cannot be compared")
    resources = summary["resources"]
    result = {}
    for name, (section, key) in METRICS.items():
        value = resources.get(section)
        if key is not None:
            value = value.get(key) if isinstance(value, dict) else None
        result[name] = metric_mean(value, name)
    processes = resources.get("host_process_cpu")
    if not isinstance(processes, dict) or not processes:
        raise ValueError("host process CPU missing")
    counts = {p.get("samples") for p in processes.values()}
    if len(counts) != 1:
        raise ValueError("host process CPU coverage differs; cannot sum unmatched intervals")
    result["host_tree_cpu_one_core_percent"] = sum(
        metric_mean(value, "host process CPU") for value in processes.values())
    return result


def read_run(directory):
    directory = Path(directory)
    metadata = json.loads((directory / "metadata.json").read_text())
    summary = json.loads((directory / "summary.json").read_text())
    with (directory / "samples.jsonl").open() as stream:
        rows = [json.loads(line) for line in stream]
    if len(rows) != summary.get("sample_count"):
        raise ValueError("raw sample count differs from summary")
    if not rows or len(rows) > 86401:
        raise ValueError("invalid raw sample count")
    errors = [error for row in rows for error in row.get("errors", [])]
    evaluated = collector.assessment(rows, errors, duration=metadata["duration_seconds"],
                                     interval=metadata["interval_seconds"])
    if not evaluated["resource_window_valid"]:
        raise ValueError("raw observations do not form a valid resource window")
    calculated = collector.resource_summary(rows)
    for key in ("host_process_cpu", "tablet_cpu", "tablet_memory", "sampled_decoder_proxies"):
        if calculated[key] != summary.get("resources", {}).get(key):
            raise ValueError(f"raw observations do not match summary: {key}")
    result = {"metadata": metadata, "summary": summary}
    run_metrics(result)
    return result


def artifact(metadata, name):
    value = metadata.get(name)
    if name == "installed_apk_sha256":
        if not isinstance(value, list) or not value:
            raise ValueError("installed APK hashes missing")
        values = value
    else:
        values = [value]
    if any(not isinstance(v, str) or len(v) != 64
           or any(c not in "0123456789abcdef" for c in v) for v in values):
        raise ValueError(f"invalid artifact identity: {name}")
    return tuple(sorted(values))


def sign_test(deltas):
    # Exact one-sided paired sign test, excluding exact ties. Runs, not frames,
    # must be independent; the tool cannot establish experimental independence.
    n = sum(value != 0 for value in deltas)
    positive = sum(value > 0 for value in deltas)
    if n == 0:
        return 1.0
    return sum(math.comb(n, k) for k in range(positive, n + 1)) / (2 ** n)


def compare_runs(baseline, candidate):
    if not baseline or len(baseline) != len(candidate) or len(baseline) > 1000:
        raise ValueError("require 1..1000 paired baseline/candidate runs")
    all_runs = baseline + candidate
    settings = ("duration_seconds", "warmup_seconds", "interval_seconds",
                "detail_interval_seconds", "workload", "tablet_properties")
    reference = baseline[0]["metadata"]
    for key in settings:
        if key not in reference or any(run["metadata"].get(key) != reference[key]
                                       for run in all_runs):
            raise ValueError(f"unmatched measurement setting: {key}")
    identities = []
    for group in (baseline, candidate):
        identity = {name: artifact(group[0]["metadata"], name)
                    for name in ("host_sha256", "installed_apk_sha256")}
        for run in group[1:]:
            if any(artifact(run["metadata"], key) != value for key, value in identity.items()):
                raise ValueError("artifact identity changed within a comparison group")
        identities.append(identity)
    before = [run_metrics(run) for run in baseline]
    after = [run_metrics(run) for run in candidate]
    metrics = {}
    enough = len(baseline) >= 10
    for name in before[0]:
        deltas = [b[name] - a[name] for a, b in zip(before, after)]
        metrics[name] = {
            "baseline_mean": statistics.mean(row[name] for row in before),
            "candidate_mean": statistics.mean(row[name] for row in after),
            "mean_delta": statistics.mean(deltas),
            "paired_deltas": deltas,
            "one_sided_sign_test_p": sign_test(deltas) if enough else None,
            "holm_adjusted_p": None,
        }
    regression = False
    if enough:
        ordered = sorted(metrics.values(), key=lambda value: value["one_sided_sign_test_p"])
        running = 0.0
        for index, value in enumerate(ordered):
            running = max(running, min(1.0, (len(ordered) - index)
                                      * value["one_sided_sign_test_p"]))
            value["holm_adjusted_p"] = running
            regression |= running <= 0.05 and value["mean_delta"] > 0
    return {
        "schema_version": 1, "acceptance": "BLOCKED",
        "comparison_unit": "paired run means", "paired_runs": len(baseline),
        "verdict": "INSUFFICIENT_RUNS" if not enough else (
            "REGRESSION" if regression else "INCONCLUSIVE"),
        "metrics": metrics,
        "artifact_change": {
            "host": identities[0]["host_sha256"] != identities[1]["host_sha256"],
            "tablet": identities[0]["installed_apk_sha256"] != identities[1]["installed_apk_sha256"],
        },
        "interpretation": (
            "One-sided exact paired sign tests with Holm familywise correction at 0.05. "
            "At least 10 paired runs required for testing; run independence and randomization "
            "must be established externally. Non-significance is not equivalence. "
            "No approved equivalence sensitivity, controlled workload proof, power proxy "
            "or input/delivery coverage is supplied by this resource comparison."
        ),
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, nargs="+", required=True)
    parser.add_argument("--candidate", type=Path, nargs="+", required=True)
    args = parser.parse_args(argv)
    try:
        paths = [p.resolve() for p in args.baseline + args.candidate]
        if len(set(paths)) != len(paths):
            raise ValueError("each run directory must be distinct; do not duplicate evidence")
        result = compare_runs([read_run(p) for p in args.baseline],
                              [read_run(p) for p in args.candidate])
        print(json.dumps(result, indent=2, allow_nan=False))
    except (OSError, ValueError, KeyError, TypeError) as exc:
        print(f"Comparison rejected: {exc}", file=sys.stderr)
        return 1
    return 2  # Results are descriptive/regression evidence, never acceptance PASS.


if __name__ == "__main__":
    sys.exit(main())
