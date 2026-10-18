package com.drawsplay.tablet.video

import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.zip.DataFormatException
import java.util.zip.Inflater

/**
 * Decodes lossless sparse-tile packets (native/drawsplay-media/PROTOCOL.md,
 * TILE payload): a zlib stream of tile records, each tile's RGB rows
 * Sub-filtered. Pure Kotlin so it can be unit-tested off-device.
 */
class TileDecoder(private val sink: Sink) {
    interface Sink {
        /** A full refresh starts: the framebuffer is [width] x [height]. */
        fun onFullBegin(width: Int, height: Int, tileSize: Int)

        /** One decoded tile; [argb] holds [w] x [h] opaque pixels, row-major. */
        fun onTile(tx: Int, ty: Int, w: Int, h: Int, argb: IntArray)

        /** The full refresh is complete; the layer may be shown. */
        fun onFullEnd()

        /** All tiles of the packet were delivered. */
        fun onPacketDone() {}
    }

    private val inflater = Inflater()
    private var raw = ByteArray(0)
    private var pixels = IntArray(0)

    fun decode(payload: ByteArray) {
        if (payload.size < HEADER) throw IllegalArgumentException("short tile packet")
        val bb = ByteBuffer.wrap(payload).order(ByteOrder.LITTLE_ENDIAN)
        val version = bb.get().toInt() and 0xFF
        if (version != 1) throw IllegalArgumentException("unsupported tile version $version")
        val flags = bb.get().toInt() and 0xFF
        val width = bb.short.toInt() and 0xFFFF
        val height = bb.short.toInt() and 0xFFFF
        val tileSize = bb.short.toInt() and 0xFFFF
        val nTiles = bb.short.toInt() and 0xFFFF
        bb.int // frameId
        val rawLen = bb.int
        if (tileSize <= 0 || rawLen < 0 || rawLen > MAX_RAW) throw IllegalArgumentException("bad tile header")
        if (raw.size < rawLen) raw = ByteArray(rawLen)
        inflater.reset()
        inflater.setInput(payload, HEADER, payload.size - HEADER)
        var got = 0
        try {
            while (got < rawLen) {
                val n = inflater.inflate(raw, got, rawLen - got)
                if (n == 0 && (inflater.finished() || inflater.needsInput() || inflater.needsDictionary())) break
                got += n
            }
        } catch (e: DataFormatException) {
            throw IllegalArgumentException("corrupt tile stream", e)
        }
        if (got != rawLen) throw IllegalArgumentException("tile stream short: $got of $rawLen")

        if (flags and FLAG_FULL_BEGIN != 0) sink.onFullBegin(width, height, tileSize)
        var pos = 0
        repeat(nTiles) {
            if (pos + 4 > rawLen) throw IllegalArgumentException("truncated tile record")
            val tx = (raw[pos].toInt() and 0xFF) or ((raw[pos + 1].toInt() and 0xFF) shl 8)
            val ty = (raw[pos + 2].toInt() and 0xFF) or ((raw[pos + 3].toInt() and 0xFF) shl 8)
            pos += 4
            val w = minOf(tileSize, width - tx * tileSize)
            val h = minOf(tileSize, height - ty * tileSize)
            if (w <= 0 || h <= 0) throw IllegalArgumentException("tile outside frame")
            val rowBytes = w * 3
            if (pos + rowBytes * h > rawLen) throw IllegalArgumentException("truncated tile data")
            if (pixels.size < w * h) pixels = IntArray(w * h)
            for (row in 0 until h) {
                val base = pos + row * rowBytes
                for (i in 3 until rowBytes) {
                    raw[base + i] = (raw[base + i] + raw[base + i - 3]).toByte()
                }
                var o = row * w
                var s = base
                for (x in 0 until w) {
                    pixels[o++] = (0xFF shl 24) or
                        ((raw[s].toInt() and 0xFF) shl 16) or
                        ((raw[s + 1].toInt() and 0xFF) shl 8) or
                        (raw[s + 2].toInt() and 0xFF)
                    s += 3
                }
            }
            pos += rowBytes * h
            sink.onTile(tx, ty, w, h, pixels)
        }
        sink.onPacketDone()
        if (flags and FLAG_FULL_END != 0) sink.onFullEnd()
    }

    fun release() = inflater.end()

    companion object {
        const val HEADER = 18
        const val FLAG_FULL_BEGIN = 1
        const val FLAG_FULL_END = 2
        private const val MAX_RAW = 32 * 1024 * 1024
    }
}
