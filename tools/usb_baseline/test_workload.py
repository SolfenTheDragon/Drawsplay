import unittest
from contextlib import redirect_stderr
import io
import json
from pathlib import Path
import subprocess
import tempfile
from unittest.mock import Mock, patch

import workload


XRANDR = """Screen 0: current 3120 x 1080
eDP-1 connected primary 1920x1080+0+0 (normal)
Virtual-1-1 connected 1200x800+1920+0 (normal)
HDMI-1 disconnected (normal)
"""


class WorkloadTests(unittest.TestCase):
    def test_only_selected_active_nonprimary_output_is_used(self):
        output = workload.virtual_output(XRANDR, "Virtual-1-1")
        self.assertEqual(output, (1200, 800, 1920, 0))

    def test_primary_missing_or_overlapping_output_is_rejected(self):
        for text, name in (
            (XRANDR, "eDP-1"), (XRANDR, "not-present"),
            (XRANDR.replace("+1920+0", "+0+0"), "Virtual-1-1"),
            (XRANDR.replace("connected 1200x800", "connected"), "Virtual-1-1"),
        ):
            with self.subTest(name=name), self.assertRaises(ValueError):
                workload.virtual_output(text, name)

    def test_scene_uses_local_lavfi_and_exact_bounded_geometry(self):
        args = workload.player_args("ffplay", (1200, 800, 1920, 0), 60, 330)
        self.assertEqual(args[0], "ffplay")
        self.assertEqual(args[args.index("-i") + 1], "testsrc2=size=1200x800:rate=60")
        self.assertEqual(args[args.index("-left") + 1], "1920")
        self.assertEqual(args[args.index("-t") + 1], "330")
        self.assertIn("-autoexit", args)
        self.assertIn("-an", args)
        self.assertIn("-noborder", args)
        self.assertNotIn("-fs", args)
        self.assertFalse(any("://" in value for value in args))

    def test_workload_bounds_prevent_accidental_huge_scene(self):
        for geometry, fps, seconds in (
            ((0, 800, 1920, 0), 60, 300),
            ((10000, 800, 1920, 0), 60, 300),
            ((1200, 800, 1920, 0), 1000, 300),
            ((1200, 800, 1920, 0), 60, float("inf")),
        ):
            with self.subTest(geometry=geometry, fps=fps), self.assertRaises(ValueError):
                workload.player_args("ffplay", geometry, fps, seconds)

    def test_display_name_is_not_an_arbitrary_physical_output(self):
        with self.assertRaises(ValueError):
            workload.virtual_output(XRANDR.replace("Virtual-1-1", "HDMI-1"), "HDMI-1")

    def test_early_player_exit_fails_before_sampling_or_recording_a_valid_run(self):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "new"
            with patch.dict("workload.os.environ", {"XDG_SESSION_TYPE": "x11", "DISPLAY": ":0"}), \
                    patch("workload.shutil.which", return_value="/bin/true"), \
                    patch("workload.collector.command", side_effect=[XRANDR, "test player"]), \
                    patch("workload.collector.read_status", return_value={
                        "aoa": {"state": "linked"},
                        "video": {"streaming": True, "displayMode": "extend"},
                    }), \
                    patch("workload.subprocess.Popen", wraps=workload.subprocess.Popen) as launch, \
                    redirect_stderr(io.StringIO()):
                result = workload.main([
                    "--host-pid", "1", "--serial", "not-used",
                    "--output", str(output), "--warmup", "1", "--duration", "1"])
            self.assertEqual(result, 1)
            self.assertFalse(output.exists())
            self.assertIsNone(launch.call_args.kwargs.get("stderr"))

    def test_player_loss_invalidates_resource_window(self):
        player = Mock()
        player.poll.side_effect = [None, 0, 0]
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "new"

            def sample(_):
                output.mkdir()
                (output / "summary.json").write_text(json.dumps({
                    "resource_window_valid": True, "resource_window_issues": []}))
                return 2

            with patch.dict("workload.os.environ", {"XDG_SESSION_TYPE": "x11", "DISPLAY": ":0"}), \
                    patch("workload.shutil.which", return_value="/bin/true"), \
                    patch("workload.collector.command", side_effect=[XRANDR, "test player"]), \
                    patch("workload.collector.read_status", return_value={
                        "aoa": {"state": "linked"},
                        "video": {"streaming": True, "displayMode": "extend"},
                    }), patch("workload.subprocess.Popen", return_value=player), \
                    patch("workload.time.sleep"), patch("workload.collector.main", side_effect=sample), \
                    redirect_stderr(io.StringIO()):
                result = workload.main([
                    "--host-pid", "1", "--serial", "not-used",
                    "--output", str(output), "--warmup", "1", "--duration", "1"])
            summary = json.loads((output / "summary.json").read_text())
            self.assertEqual(result, 1)
            self.assertFalse(summary["resource_window_valid"])
            self.assertEqual(summary["resource_window_issues"],
                             ["player exited before collection completed"])
            self.assertIsNotNone(json.loads((output / "workload.json").read_text())["error"])
            player.communicate.assert_called_once_with(timeout=5)

    def test_collector_failure_stops_owned_player_with_bounded_wait(self):
        player = Mock()
        player.poll.return_value = None
        player.communicate.side_effect = [
            subprocess.TimeoutExpired("test player", 5), (None, None)]
        with tempfile.TemporaryDirectory() as temp:
            with patch.dict("workload.os.environ", {"XDG_SESSION_TYPE": "x11", "DISPLAY": ":0"}), \
                    patch("workload.shutil.which", return_value="/bin/true"), \
                    patch("workload.collector.command", side_effect=[XRANDR, "test player"]), \
                    patch("workload.collector.read_status", return_value={
                        "aoa": {"state": "linked"},
                        "video": {"streaming": True, "displayMode": "extend"},
                    }), patch("workload.subprocess.Popen", return_value=player), \
                    patch("workload.time.sleep"), \
                    patch("workload.collector.main", side_effect=RuntimeError("test failure")), \
                    redirect_stderr(io.StringIO()):
                result = workload.main([
                    "--host-pid", "1", "--serial", "not-used",
                    "--output", str(Path(temp) / "new"), "--warmup", "1", "--duration", "1"])
            self.assertEqual(result, 1)
            player.terminate.assert_called_once_with()
            player.kill.assert_called_once_with()
            self.assertEqual(player.communicate.call_count, 2)


if __name__ == "__main__":
    unittest.main()
