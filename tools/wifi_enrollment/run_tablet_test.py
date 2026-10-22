#!/usr/bin/env python3
"""Opt-in actual-LAN enrollment test; never replaces the installed display app."""

import argparse
import json
import pathlib
import re
import selectors
import subprocess
import tempfile
import time
import xml.etree.ElementTree as ET

PACKAGE = "com.drawsplay.tablet.wifitest"
ACTIVITY = PACKAGE + "/com.drawsplay.tablet.WiFiEnrollmentProbeActivity"
REMOTE_UI = "/data/local/tmp/drawsplay-enrollment-test-ui.xml"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pair-command", type=pathlib.Path, required=True)
    parser.add_argument("--apk", type=pathlib.Path, required=True)
    parser.add_argument("--interface", required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--verify-session", action="store_true",
                        help="also require fresh saved-trust control/video association; no capture or input")
    args = parser.parse_args()
    devices = subprocess.check_output(["adb", "devices"], text=True)
    attached = [line.split()[0] for line in devices.splitlines()[1:] if line.endswith("\tdevice")]
    if len(attached) != 1:
        raise RuntimeError("Require exactly one authorized attached tablet; no device selected implicitly")
    adb = ["adb", "-s", attached[0]]

    def command(*arguments):
        return subprocess.check_output([*adb, *arguments], text=True, timeout=30)

    if f"package:{PACKAGE}" in command("shell", "pm", "list", "packages", PACKAGE).splitlines():
        raise RuntimeError("Isolated test package already exists; refusing to replace its data")
    installed = False
    process = None
    try:
        # No -r: installation must never overwrite an existing application.
        if "Success" not in command("install", "--no-streaming", str(args.apk.resolve())):
            raise RuntimeError("Test APK installation did not succeed")
        installed = True
        command("shell", "am", "start", "-W", "-n", ACTIVITY,
                "--ez", "checkSession", str(args.verify_session).lower())
        pid = command("shell", "pidof", PACKAGE).strip()
        if not pid.isdigit():
            raise RuntimeError("Test application did not start as a single process")

        def logs():
            return command("logcat", "-d", "--pid", pid, "-s", "DS-WiFiEnrollment:I", "*:S")

        end = time.monotonic() + 15
        while True:
            events = [json.loads(line[line.index("{"):]) for line in logs().splitlines() if '{"state":' in line]
            available = next((event for event in events if event["state"] == "available"), None)
            if available:
                break
            if time.monotonic() >= end:
                raise RuntimeError("Tablet advertisement was not ready: " + logs())
            time.sleep(0.2)
        with tempfile.TemporaryDirectory(prefix="drawsplay-wifi-enrollment-") as temporary:
            directory = pathlib.Path(temporary)
            with (directory / "diagnostics.log").open("w+") as diagnostics:
                process = subprocess.Popen(
                    [str(args.pair_command.resolve()), "-interface", args.interface,
                     "-tablet", available["name"], "-trust-dir", str(directory / "trust"),
                     *(["-verify-session"] if args.verify_session else [])],
                    stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=diagnostics,
                )
                selector = selectors.DefaultSelector()
                greeting = b""
                try:
                    selector.register(process.stdout, selectors.EVENT_READ)
                    end = time.monotonic() + 20
                    while b"\n" not in greeting:
                        if time.monotonic() >= end or not selector.select(1):
                            if time.monotonic() < end:
                                continue
                            raise RuntimeError("Host did not open numeric pairing in time")
                        chunk = process.stdout.read1(1024)
                        if not chunk:
                            diagnostics.seek(0)
                            raise RuntimeError("Host could not discover/open pairing: " + diagnostics.read())
                        greeting += chunk
                        if len(greeting) > 4096:
                            raise RuntimeError("Host enrollment output exceeded bounds")
                finally:
                    selector.close()
                code_match = re.match(rb"Pairing code: ([0-9]{8})\n", greeting)
                if not code_match:
                    raise RuntimeError("Host did not produce the expected numeric-pairing prompt")
                code = code_match[1].decode("ascii")
                command("shell", "uiautomator", "dump", REMOTE_UI)
                tree = ET.fromstring(command("shell", "cat", REMOTE_UI))
                entry = next(node for node in tree.iter("node") if node.get("class") == "android.widget.EditText")
                approve = next(node for node in tree.iter("node") if node.get("text", "").casefold() == "pair and trust this host")

                def tap(node):
                    x1, y1, x2, y2 = map(int, re.findall(r"\d+", node.attrib["bounds"]))
                    command("shell", "input", "tap", str((x1 + x2) // 2), str((y1 + y2) // 2))

                tap(entry)
                command("shell", "input", "text", code)
                command("shell", "input", "keyevent", "111")  # Dismiss IME, not the activity.
                tap(approve)
                # This is explicit synthetic approval in a disposable test, not
                # an automatic-approval option in the product pairing command.
                try:
                    output, _ = process.communicate(input=b"yes\n", timeout=35)
                except subprocess.TimeoutExpired as error:
                    diagnostics.seek(0)
                    raise RuntimeError("Enrollment timed out: " + diagnostics.read() + logs()) from error
                diagnostics.seek(0)
                if process.returncode:
                    raise RuntimeError("Host enrollment failed: " + diagnostics.read() + logs())
                if b"Pairing saved on both devices." not in output:
                    raise RuntimeError("Host did not confirm durable trust")
                if args.verify_session and b"Paired control/video channels verified using saved trust." not in output:
                    raise RuntimeError("Host did not confirm fresh authenticated control/video association")
                record = json.loads((directory / "trust" / "trust.json").read_text())
                if not record.get("tabletSpki") or record.get("tabletLabel") != available["name"]:
                    raise RuntimeError("Host trust did not persist the selected tablet")
                end = time.monotonic() + 5
                while True:
                    device_logs = logs()
                    session_ready = not args.verify_session or '"state":"sessionReady"' in device_logs
                    if '"state":"paired"' in device_logs and session_ready and "multicast lock released" in device_logs:
                        break
                    if time.monotonic() >= end:
                        raise RuntimeError("Tablet did not confirm trust/cleanup: " + device_logs)
                    time.sleep(0.1)
                report = {
                    "device": command("shell", "getprop", "ro.product.model").strip(),
                    "androidApi": int(command("shell", "getprop", "ro.build.version.sdk").strip()),
                    "hostDiscoveredTablet": True,
                    "hostInitiatedTcp": True,
                    "numericPairingOverLan": True,
                    "mutualTlsWithKeystoreIdentity": True,
                    "hostTrustPersisted": True,
                    "tabletTrustReloaded": True,
                    "tabletIdentityRetained": True,
                    "freshSavedTrustControlAndVideoVerified": args.verify_session,
                    "networkAndMulticastCleanupConfirmed": True,
                    "approval": "automated local test input on both devices; disposable identities",
                    "usbImplementationTested": False,
                    "videoOrInputSessionStarted": False,
                    "limitations": "One API 34/Linux IPv4 LAN run; not streaming, power, soak, Android 8 or Windows evidence",
                }
                args.output.write_text(json.dumps(report, indent=2) + "\n")
    finally:
        if process is not None and process.poll() is None:
            process.terminate()
            process.communicate(timeout=5)
        if installed:
            command("shell", "rm", "-f", REMOTE_UI)
            if "Success" not in command("uninstall", PACKAGE):
                raise RuntimeError("Isolated test APK cleanup failed")
            if f"package:{PACKAGE}" in command("shell", "pm", "list", "packages", PACKAGE).splitlines():
                raise RuntimeError("Isolated test APK remained installed after cleanup")
    print("LAN enrollment and durable trust passed; isolated APK and disposable host identity removed.")


if __name__ == "__main__":
    main()
