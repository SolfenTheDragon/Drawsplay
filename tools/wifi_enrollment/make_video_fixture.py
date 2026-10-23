#!/usr/bin/env python3
"""Generate local synthetic AVC only. Never captures a desktop or camera."""

import argparse
import pathlib
import re
import struct
import subprocess
import tempfile


def packetize(raw, frames):
    starts = list(re.finditer(b"\x00\x00\x00?\x01", raw))
    pictures = []
    current = bytearray()
    sps = pps = None
    for index, marker in enumerate(starts):
        end = starts[index + 1].start() if index + 1 < len(starts) else len(raw)
        unit = raw[marker.start():end]
        nal = raw[marker.end()] & 31
        if nal in (7, 8):
            if nal == 7:
                if sps is not None and sps != unit:
                    raise ValueError("Synthetic SPS changed within fixture")
                sps = unit
            else:
                if pps is not None and pps != unit:
                    raise ValueError("Synthetic PPS changed within fixture")
                pps = unit
        else:
            if nal == 9 and current:
                pictures.append(bytes(current))
                current.clear()
            current.extend(unit)
    if current:
        pictures.append(bytes(current))
    if not sps or not pps or len(pictures) != frames:
        raise ValueError("Encoder did not produce the expected SPS/PPS and complete AUD pictures")
    if any(not 1 <= len(picture) <= 262144 for picture in pictures):
        raise ValueError("Synthetic picture exceeds negotiated AU limit")
    config = struct.pack(">6HI", 1, 640, 360, 30, len(sps), len(pps), 0) + sps + pps
    output = bytearray(struct.pack(">4sIII", b"DSVT", frames, len(config), 0) + config)
    for picture in pictures:
        output.extend(struct.pack(">I", len(picture)))
        output.extend(picture)
    return output


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--frames", type=int, default=300)
    args = parser.parse_args()
    if not 60 <= args.frames <= 1800:
        parser.error("--frames must be 60..1800")
    if args.output.exists():
        parser.error("output already exists; refusing to overwrite")
    with tempfile.TemporaryDirectory(prefix="drawsplay-synthetic-video-") as directory:
        raw = pathlib.Path(directory) / "test.h264"
        subprocess.run([
            "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30",
            "-frames:v", str(args.frames), "-c:v", "libx264", "-profile:v", "baseline",
            "-level", "3.1", "-preset", "ultrafast", "-tune", "zerolatency",
            "-pix_fmt", "yuv420p", "-x264-params",
            "aud=1:repeat-headers=1:keyint=30:min-keyint=30:scenecut=0",
            "-f", "h264", str(raw)
        ], check=True, timeout=120)
        with args.output.open("xb") as output:
            output.write(packetize(raw.read_bytes(), args.frames))
    print(f"Created {args.frames} synthetic 640x360/30 pictures; no screen content captured.")


if __name__ == "__main__":
    main()
