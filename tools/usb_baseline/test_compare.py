import json
from pathlib import Path
import tempfile
import unittest
from contextlib import redirect_stderr
import io
from unittest.mock import patch

import compare


def evidence(offset=0):
    return {
        "metadata": {
            "schema_version": 2, "duration_seconds": 300, "warmup_seconds": 300,
            "interval_seconds": 2, "detail_interval_seconds": 30,
            "workload": "synthetic-scene-v1", "host_sha256": "a" * 64,
            "installed_apk_sha256": ["b" * 64],
            "tablet_properties": {"ro.product.model": "SM-X200", "ro.build.version.sdk": "34"},
        },
        "summary": {
            "resource_window_valid": True, "sample_count": 3, "error_count": 0,
            "resources": {
                "host_process_cpu": {
                    "10:1": {"mean": 4.0 + offset, "samples": 2},
                    "20:2": {"mean": 10.0 + offset, "samples": 2},
                },
                "tablet_cpu": {"mean": 48.0 + offset, "samples": 2},
                "tablet_memory": {
                    "pss_kib": {"mean": 54000 + offset, "samples": 3},
                    "rss_kib": {"mean": 100000 + offset, "samples": 3},
                },
                "sampled_decoder_proxies": {
                    "avgDecodeMs": {"mean": 26.0 + offset, "samples": 3},
                    "queuedFrames": {"mean": offset, "samples": 3},
                },
            },
        },
    }


class ComparisonTests(unittest.TestCase):
    def test_run_metrics_sum_host_processes_and_do_not_join_pids_across_runs(self):
        result = compare.run_metrics(evidence())
        self.assertEqual(result["host_tree_cpu_one_core_percent"], 14.0)
        self.assertEqual(result["tablet_cpu_one_core_percent"], 48.0)
        self.assertEqual(result["tablet_pss_kib"], 54000)
        self.assertEqual(result["sampled_decode_ms"], 26.0)

    def test_invalid_or_missing_metric_cannot_become_zero(self):
        for mutate in (
            lambda run: run["summary"].update(resource_window_valid=False),
            lambda run: run["summary"]["resources"].update(tablet_cpu=None),
            lambda run: run["summary"]["resources"]["tablet_memory"].update(pss_kib=None),
            lambda run: run["summary"]["resources"]["tablet_cpu"].update(mean=float("nan")),
        ):
            run = evidence()
            mutate(run)
            with self.subTest(mutate=mutate), self.assertRaises(ValueError):
                compare.run_metrics(run)

    def test_positive_shift_detected_at_run_level(self):
        baseline = [evidence() for _ in range(10)]
        candidate = [evidence(2) for _ in range(10)]
        result = compare.compare_runs(baseline, candidate)
        self.assertEqual(result["comparison_unit"], "paired run means")
        self.assertEqual(result["verdict"], "REGRESSION")
        self.assertGreater(result["metrics"]["tablet_cpu_one_core_percent"]["holm_adjusted_p"], 0)
        self.assertLess(result["metrics"]["tablet_cpu_one_core_percent"]["holm_adjusted_p"], 0.05)
        self.assertEqual(result["metrics"]["tablet_cpu_one_core_percent"]["mean_delta"], 2.0)

    def test_unchanged_runs_are_inconclusive_not_equivalence_pass(self):
        result = compare.compare_runs([evidence() for _ in range(10)],
                                      [evidence() for _ in range(10)])
        self.assertEqual(result["verdict"], "INCONCLUSIVE")
        self.assertEqual(result["acceptance"], "BLOCKED")
        self.assertIn("equivalence", result["interpretation"])

    def test_too_few_pairs_descriptive_only_even_if_shift_large(self):
        result = compare.compare_runs([evidence()], [evidence(100)])
        self.assertEqual(result["verdict"], "INSUFFICIENT_RUNS")
        self.assertIsNone(result["metrics"]["tablet_cpu_one_core_percent"]["holm_adjusted_p"])

    def test_pair_count_and_measurement_settings_must_match(self):
        with self.assertRaises(ValueError):
            compare.compare_runs([evidence()], [])
        for field, changed in (("workload", "other"), ("interval_seconds", 5),
                               ("duration_seconds", 60), ("warmup_seconds", 0)):
            run = evidence()
            run["metadata"][field] = changed
            with self.subTest(field=field), self.assertRaises(ValueError):
                compare.compare_runs([evidence()], [run])

    def test_device_change_or_reused_artifact_generation_is_not_silently_pooled(self):
        candidate = evidence()
        candidate["metadata"]["tablet_properties"]["ro.build.version.sdk"] = "35"
        with self.assertRaises(ValueError):
            compare.compare_runs([evidence()], [candidate])
        baseline = [evidence(), evidence()]
        baseline[1]["metadata"]["host_sha256"] = "c" * 64
        with self.assertRaises(ValueError):
            compare.compare_runs(baseline, [evidence(), evidence()])

    def test_candidate_artifact_may_differ_but_is_reported(self):
        candidate = evidence()
        candidate["metadata"]["host_sha256"] = "c" * 64
        result = compare.compare_runs([evidence()], [candidate])
        self.assertTrue(result["artifact_change"]["host"])
        self.assertFalse(result["artifact_change"]["tablet"])

    def test_large_run_count_uses_bounded_reproducible_test(self):
        baseline = [evidence() for _ in range(24)]
        candidate = [evidence(1 if i % 2 else 2) for i in range(24)]
        first = compare.compare_runs(baseline, candidate)
        self.assertEqual(first, compare.compare_runs(baseline, candidate))
        self.assertEqual(first["verdict"], "REGRESSION")

    def test_reader_rejects_summary_that_does_not_match_record_count(self):
        with tempfile.TemporaryDirectory() as temp:
            p = Path(temp)
            run = evidence()
            (p / "metadata.json").write_text(json.dumps(run["metadata"]))
            (p / "summary.json").write_text(json.dumps(run["summary"]))
            (p / "samples.jsonl").write_text('{"index":0}\n')
            with self.assertRaisesRegex(ValueError, "sample count"):
                compare.read_run(p)

    def test_nonfinite_numeric_values_in_summary_are_rejected(self):
        run = evidence()
        run["summary"]["resources"]["tablet_cpu"]["mean"] = float("inf")
        with self.assertRaises(ValueError):
            compare.compare_runs([evidence()], [run])

    def test_negative_delta_is_not_reported_as_adverse_regression(self):
        result = compare.compare_runs([evidence(2) for _ in range(10)],
                                      [evidence() for _ in range(10)])
        self.assertEqual(result["verdict"], "INCONCLUSIVE")

    def test_holm_correction_has_known_exact_value_for_ten_positive_pairs(self):
        result = compare.compare_runs([evidence() for _ in range(10)],
                                      [evidence(1) for _ in range(10)])
        cpu = result["metrics"]["tablet_cpu_one_core_percent"]
        self.assertEqual(cpu["one_sided_sign_test_p"], 1 / 1024)
        self.assertEqual(cpu["holm_adjusted_p"], 6 / 1024)

    def test_unequal_host_process_coverage_cannot_be_added(self):
        run = evidence()
        run["summary"]["resources"]["host_process_cpu"]["20:2"]["samples"] = 1
        with self.assertRaisesRegex(ValueError, "coverage"):
            compare.run_metrics(run)

    def test_duplicate_run_directory_is_rejected_before_reading(self):
        with tempfile.TemporaryDirectory() as temp, \
                patch("compare.read_run", side_effect=AssertionError("must not read duplicate")), \
                redirect_stderr(io.StringIO()):
            self.assertEqual(compare.main(["--baseline", temp, "--candidate", temp]), 1)

    def test_read_run_checks_real_record_consistency(self):
        with tempfile.TemporaryDirectory() as temp:
            p = Path(temp)
            rows = [{
                "elapsed_seconds": index,
                "status": {"aoa": {"state": "linked"}, "video": {"streaming": True}},
                "host_processes": [{"pid": 1, "start_ticks": 2,
                                    "cpu_percent_one_core": 4.0}],
                "tablet": {"cpu_percent_one_core": 48.0},
                "tablet_memory": {"pss_kib": 50, "rss_kib": 100, "swap_pss_kib": 0},
            } for index in range(3)]
            for row in rows:
                row["status"]["clientVideoStats"] = {"avgDecodeMs": 26.0, "queuedFrames": 0}
            run = evidence()
            run["metadata"].update(duration_seconds=3, interval_seconds=1)
            run["summary"]["resources"] = compare.collector.resource_summary(rows)
            (p / "metadata.json").write_text(json.dumps(run["metadata"]))
            (p / "summary.json").write_text(json.dumps(run["summary"]))
            (p / "samples.jsonl").write_text("".join(json.dumps(r) + "\n" for r in rows))
            self.assertEqual(compare.run_metrics(compare.read_run(p))[
                "host_tree_cpu_one_core_percent"], 4.0)
            rows[1]["tablet"]["cpu_percent_one_core"] = 100
            (p / "samples.jsonl").write_text("".join(json.dumps(r) + "\n" for r in rows))
            with self.assertRaisesRegex(ValueError, "match summary"):
                compare.read_run(p)


if __name__ == "__main__":
    unittest.main()
