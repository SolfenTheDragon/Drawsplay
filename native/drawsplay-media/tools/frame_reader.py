#!/usr/bin/env python3
"""Reads drawsplay-media framed stdout from stdin and reports statistics.

  drawsplay-media ... | frame_reader.py --h264 out.h264 [--ppm tiles.ppm] [--seconds 5]

Reports per-kind counts/bytes, pipeUs percentiles, inputSeq tags and mode
changes. Kind-1 payloads are concatenated into --h264. Kind-2 payloads are
inflated and un-filtered into an RGB framebuffer that is saved to --ppm.
"""
import argparse
import struct
import sys
import time
import zlib


def read_exact(f, n):
    buf = b""
    while len(buf) < n:
        chunk = f.read(n - len(buf))
        if not chunk:
            return None
        buf += chunk
    return buf


def pct(v, p):
    if not v:
        return 0
    s = sorted(v)
    return s[min(len(s) - 1, int(len(s) * p / 100))]


def apply_tile(fb, payload):
    ver, flags, w, h, ts, n, fid, raw_len = struct.unpack_from("<BBHHHHII", payload, 0)
    assert ver == 1
    raw = zlib.decompress(payload[18:])
    assert len(raw) == raw_len, (len(raw), raw_len)
    if fb is None or fb["w"] != w or fb["h"] != h or flags & 1:
        fb = {"w": w, "h": h, "px": bytearray(w * h * 3)}
    p = 0
    for _ in range(n):
        tx, ty = struct.unpack_from("<HH", raw, p)
        p += 4
        x0, y0 = tx * ts, ty * ts
        x1, y1 = min(w, x0 + ts), min(h, y0 + ts)
        rb = (x1 - x0) * 3
        for y in range(y0, y1):
            row = bytearray(raw[p:p + rb])
            p += rb
            for i in range(3, rb):
                row[i] = (row[i] + row[i - 3]) & 255
            o = (y * w + x0) * 3
            fb["px"][o:o + rb] = row
    assert p == len(raw)
    return fb, flags, fid


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--h264")
    ap.add_argument("--ppm")
    ap.add_argument("--seconds", type=float, default=0, help="stop reading after this many seconds")
    ap.add_argument("--trace", action="store_true", help="print every frame")
    ap.add_argument("--cmd-fd", type=int, default=-1)
    a = ap.parse_args()

    inp = sys.stdin.buffer
    out = open(a.h264, "wb") if a.h264 else None
    stats = {}
    pipe = {1: [], 2: []}
    seqs = {}
    modes = []
    first = None
    fb = None
    order = []
    t_start = time.monotonic()
    t_first = None
    idrs = 0
    sizes = []

    while True:
        if a.seconds and time.monotonic() - t_start > a.seconds:
            break
        hdr = read_exact(inp, 16)
        if hdr is None:
            break
        plen, kind, flags, resv, seq, pipe_us = struct.unpack("<IBBHII", hdr)
        payload = read_exact(inp, plen)
        if payload is None:
            print("truncated frame", file=sys.stderr)
            return 1
        now = time.monotonic()
        if t_first is None:
            t_first = now
        st = stats.setdefault(kind, [0, 0])
        st[0] += 1
        st[1] += plen
        if first is None:
            first = (kind, flags)
        if kind == 1:
            pipe[1].append(pipe_us)
            sizes.append(plen)
            if flags & 1:
                idrs += 1
            if out:
                out.write(payload)
            if seq:
                seqs.setdefault(seq, 0)
                seqs[seq] += 1
            order.append("I" if flags & 1 else "P")
        elif kind == 2:
            pipe[2].append(pipe_us)
            fb, fl, fid = apply_tile(fb, payload)
            order.append("T%s" % ("B" if fl & 1 else "") + ("E" if fl & 2 else ""))
        elif kind == 3:
            modes.append(payload[0])
            order.append("M%d" % payload[0])
        if a.trace:
            print(f"t={now - t_first:7.3f} kind={kind} flags={flags} len={plen} seq={seq} pipeUs={pipe_us}")

    elapsed = (time.monotonic() - t_first) if t_first else 0
    print(f"first frame: kind={first[0]} flags={first[1]}" if first else "no frames")
    for k, (n, b) in sorted(stats.items()):
        print(f"kind {k}: {n} frames, {b} bytes")
    for k in (1, 2):
        if pipe[k]:
            print(f"kind {k} pipeUs: min={min(pipe[k])} p50={pct(pipe[k], 50)} p95={pct(pipe[k], 95)} "
                  f"p99={pct(pipe[k], 99)} max={max(pipe[k])}")
    if sizes:
        print(f"AU sizes: mean={sum(sizes) // len(sizes)} max={max(sizes)} idrs={idrs}")
    if elapsed and stats.get(1):
        print(f"AU rate: {stats[1][0] / elapsed:.1f}/s, bitrate {stats[1][1] * 8 / elapsed / 1000:.0f} kbps over {elapsed:.2f}s")
    if seqs:
        print("inputSeq tags:", dict(sorted(seqs.items())))
    if modes:
        print("modes:", modes)
    print("order:", " ".join(order[:60]) + (" ..." if len(order) > 60 else ""))
    if a.ppm and fb:
        with open(a.ppm, "wb") as f:
            f.write(b"P6\n%d %d\n255\n" % (fb["w"], fb["h"]))
            f.write(fb["px"])
    if out:
        out.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
