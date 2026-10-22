import json
import os
from pathlib import Path
from contextlib import redirect_stderr
import io
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import collect


def proc_stat(pid=12, start=50, ticks=100, rss=42, name="encoder (worker)"):
    fields = ["S"] + ["0"] * 49
    fields[1] = "1"
    fields[11] = str(ticks // 2)
    fields[12] = str(ticks - ticks // 2)
    fields[19] = str(start)
    fields[21] = str(rss)
    return f"{pid} ({name}) " + " ".join(fields)


class ProcessTests(unittest.TestCase):
    def test_empty_uptime_is_a_validation_error(self):
        with self.assertRaises(ValueError):
            collect.parse_tablet_process("12\n \n" + proc_stat())

    def test_tablet_pid_must_match_stat(self):
        stamp, stat = collect.parse_tablet_process("12\n25.5 100\n" + proc_stat())
        self.assertEqual((stamp, stat.pid), (25.5, 12))
        for text in ("13\n25.5 100\n" + proc_stat(), "12 13\n25.5 100\n" + proc_stat(),
                     "12\nnan 100\n" + proc_stat()):
            with self.subTest(text=text), self.assertRaises(ValueError):
                collect.parse_tablet_process(text)

    def test_parses_comm_with_spaces_and_parentheses(self):
        stat = collect.parse_stat(proc_stat())
        self.assertEqual((stat.pid, stat.ppid, stat.start_ticks), (12, 1, 50))
        self.assertEqual((stat.cpu_ticks, stat.rss_pages), (100, 42))

    def test_cpu_uses_measured_time_and_can_exceed_one_core(self):
        before = collect.parse_stat(proc_stat(ticks=100))
        after = collect.parse_stat(proc_stat(ticks=500))
        self.assertEqual(collect.cpu_percent((10.0, before), (12.0, after), 100), 200.0)

    def test_pid_reuse_is_not_a_cpu_sample(self):
        before = collect.parse_stat(proc_stat(start=50))
        after = collect.parse_stat(proc_stat(start=60, ticks=120))
        with self.assertRaisesRegex(ValueError, "generation"):
            collect.cpu_percent((1, before), (2, after), 100)

    def test_zero_elapsed_and_decreasing_ticks_are_errors(self):
        before = collect.parse_stat(proc_stat(ticks=100))
        after = collect.parse_stat(proc_stat(ticks=90))
        with self.assertRaises(ValueError):
            collect.cpu_percent((1, before), (1, before), 100)
        with self.assertRaises(ValueError):
            collect.cpu_percent((1, before), (2, after), 100)

    def test_truncated_or_malformed_stat_is_error(self):
        for value in ("", "12 (worker) S", "bad (worker) S"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                collect.parse_stat(value)


class EvidenceTests(unittest.TestCase):
    def test_empty_apk_hash_is_a_validation_error(self):
        def adb(script, label):
            if script == "pm path com.drawsplay.tablet":
                return "package:/data/app/base.apk"
            return ""
        with patch("collect.command", return_value="test"):
            with self.assertRaises(ValueError):
                collect.identity(os.getpid(), adb)

    def test_sparse_or_incomplete_sampling_invalidates_window(self):
        status = {"aoa": {"state": "linked"}, "video": {"streaming": True}}
        for times in ((0, 1, 9), (0, 1, 2), (4, 5, 6, 7, 8, 9)):
            rows = [{"status": status, "elapsed_seconds": t} for t in times]
            result = collect.assessment(rows, [], duration=10, interval=1)
            self.assertFalse(result["resource_window_valid"])
            self.assertIn("sampling coverage gap", result["resource_window_issues"])

    def test_host_generation_change_during_warmup_invalidates_window(self):
        clock = [0.0]
        def sleep(seconds):
            clock[0] += seconds
        def tree(pid):
            return [collect.parse_stat(proc_stat(start=50 if clock[0] < 5 else 60))]
        def cmd(args, label, timeout=8):
            if label == "tablet clock ticks":
                return "100"
            if label == "tablet memory":
                return "TOTAL PSS: 10 TOTAL RSS: 20 TOTAL SWAP PSS: 0"
            if label == "tablet battery":
                return "level: 50\ntemperature: 250"
            raise AssertionError(label)
        status = {"aoa": {"state": "linked"}, "video": {"streaming": True}}
        with tempfile.TemporaryDirectory() as temp:
            args = SimpleNamespace(serial="private", host_pid=12, warmup=5,
                                   duration=3, interval=1, detail_interval=30,
                                   workload="test")
            with patch("collect.time.monotonic", side_effect=lambda: clock[0]), \
                    patch("collect.time.sleep", side_effect=sleep), \
                    patch("collect.host_tree", side_effect=tree), \
                    patch("collect.identity", return_value={}), \
                    patch("collect.command", side_effect=cmd), \
                    patch("collect.read_status", return_value=status), \
                    patch("collect.read_tablet", side_effect=lambda adb:
                          (clock[0], collect.parse_stat(proc_stat()))):
                result = collect.collect(args, Path(temp))
        self.assertFalse(result["resource_window_valid"])
        self.assertIn("host process tree generation changed", result["resource_window_issues"])

    def test_android_randomized_apk_directory_can_contain_tildes(self):
        def adb(script, label):
            if script == "pm path com.drawsplay.tablet":
                return "package:/data/app/~~random==/com.drawsplay.tablet-random==/base.apk\n"
            if script.startswith("sha256sum "):
                return "a" * 64 + "  /data/app/base.apk\n"
            return "test"

        with patch("collect.command", return_value="test"):
            result = collect.identity(os.getpid(), adb)
        self.assertEqual(result["installed_apk_sha256"], ["a" * 64])

    def test_status_rejects_nonfinite_or_nested_scalar_values(self):
        raw = {"metrics": {"captureRestarts": float("nan")},
               "video": {"encoder": {"token": "private"}}}
        self.assertEqual(collect.safe_status(raw), {"metrics": {}, "video": {}})

    def test_command_failure_does_not_include_serial_or_stderr(self):
        with patch("collect.subprocess.run", return_value=subprocess.CompletedProcess(
                ["adb"], 1, "", "private-serial secret")):
            with self.assertRaisesRegex(RuntimeError, "^tablet: command exit 1$"):
                collect.command(["adb", "-s", "private-serial"], "tablet")

    def test_failure_persists_explicit_blocked_report(self):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "new"
            with patch("collect.collect", side_effect=RuntimeError("tablet unavailable")), \
                    redirect_stderr(io.StringIO()):
                self.assertEqual(collect.main([
                    "--host-pid", "1", "--serial", "private", "--output", str(output),
                    "--warmup", "0", "--duration", "1"]), 1)
            report = json.loads((output / "failure.json").read_text())
            self.assertEqual(report["acceptance"], "BLOCKED")
            self.assertIn("tablet unavailable", report["error"])

    def test_meminfo_requires_all_totals_not_zero_defaults(self):
        text = " TOTAL PSS: 53355 TOTAL RSS: 106272 TOTAL SWAP PSS: 19582"
        self.assertEqual(collect.parse_meminfo(text), {
            "pss_kib": 53355, "rss_kib": 106272, "swap_pss_kib": 19582,
        })
        with self.assertRaisesRegex(ValueError, "RSS"):
            collect.parse_meminfo("TOTAL PSS: 50")

    def test_status_is_nested_allowlist(self):
        raw = {
            "token": "do-not-record", "activeClient": {"device": "private"},
            "version": "dev-test", "aoa": {"state": "linked", "serial": "private"},
            "video": {"streaming": True, "encoder": "h264", "display": "private"},
            "metrics": {"videoRestarts": 3, "password": "private"},
            "clientVideoStats": {"framesReceived": 12},
        }
        sanitized = collect.safe_status(raw)
        self.assertEqual(sanitized, {
            "version": "dev-test", "aoa": {"state": "linked"},
            "video": {"streaming": True, "encoder": "h264"},
            "metrics": {"videoRestarts": 3},
        })
        self.assertNotIn("private", json.dumps(sanitized))

    def test_complete_resource_samples_still_block_full_acceptance(self):
        result = collect.assessment([{"status": {
            "aoa": {"state": "linked"}, "video": {"streaming": True},
            "metrics": {"captureRestarts": 0},
        }}] * 2, [])
        self.assertEqual(result["acceptance"], "BLOCKED")
        self.assertTrue(result["resource_window_valid"])
        self.assertIn("end_to_end_latency", result["missing_acceptance_evidence"])

    def test_restart_or_command_error_invalidates_window(self):
        samples = [{"status": {"aoa": {"state": "linked"},
                               "video": {"streaming": True},
                               "metrics": {"captureRestarts": n}}} for n in (0, 1)]
        self.assertFalse(collect.assessment(samples, [])["resource_window_valid"])
        self.assertFalse(collect.assessment(samples[:1], ["adb timeout"])["resource_window_valid"])

    def test_missing_usb_status_blocks_window(self):
        self.assertFalse(collect.assessment([{"status": {}}] * 2, [])["resource_window_valid"])


class CLITests(unittest.TestCase):
    def test_timing_values_must_be_finite_and_positive(self):
        for value in ("nan", "inf", "-1", "0"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                collect.positive_seconds(value)
        self.assertEqual(collect.positive_seconds("0.5"), 0.5)

    def test_existing_output_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "evidence"
            output.mkdir()
            sentinel = output / "keep"
            sentinel.write_text("original")
            result = subprocess.run(
                [sys.executable, str(Path(collect.__file__)), "--host-pid", "1",
                 "--serial", "private-serial", "--output", str(output),
                 "--duration", "1", "--warmup", "0"],
                capture_output=True, text=True,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(sentinel.read_text(), "original")
            self.assertNotIn("private-serial", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
