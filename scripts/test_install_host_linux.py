import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class DependencyDetectionTest(unittest.TestCase):
    def detect(self, session, inspector=None):
        source = Path(__file__).with_name("install-host-linux.sh").read_text()
        detection = source.split("# --- Dependency detection", 1)[1]
        detection = detection.split("\n", 1)[1].split("# --- VA-API GPU driver", 1)[0]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            commands = {
                "ffmpeg": "exit 0",
                "vainfo": "exit 0",
                "xrandr": "exit 0",
                "grep": "exit 1",
            }
            if inspector is not None:
                commands["gst-launch-1.0"] = "exit 0"
                commands["gst-inspect-1.0"] = inspector
            for name, body in commands.items():
                command = root / name
                command.write_text("#!/bin/sh\n" + body + "\n")
                command.chmod(0o700)
            env = dict(os.environ, PATH=directory, HOME=directory,
                       XDG_CONFIG_HOME=directory, XDG_SESSION_TYPE=session,
                       DRAWSPLAY_ADB_TUNNEL="0")
            return subprocess.run(
                ["/bin/bash", "-c", "set -euo pipefail\nSKIP_INSTALL=1\n" + detection],
                env=env, text=True, capture_output=True, check=True,
            ).stdout

    def test_fresh_wayland_includes_plugins_without_inspector(self):
        output = self.detect("wayland")
        packages = output.split("Missing packages: ", 1)[1].splitlines()[0].split()
        self.assertEqual(set(packages), {
            "gstreamer1.0-tools", "gstreamer1.0-pipewire",
            "gstreamer1.0-plugins-base", "gstreamer1.0-plugins-good",
            "gstreamer1.0-plugins-bad", "gstreamer1.0-vaapi",
            "gstreamer1.0-plugins-ugly",
        })
        self.assertEqual(len(packages), len(set(packages)))

    def test_existing_inspector_detects_only_missing_plugins(self):
        output = self.detect("wayland", '[ "$1" != "x264enc" ]')
        self.assertIn("Missing packages: gstreamer1.0-plugins-ugly\n", output)

    def test_x11_does_not_require_gstreamer(self):
        self.assertNotIn("gstreamer", self.detect("x11"))


if __name__ == "__main__":
    unittest.main()
