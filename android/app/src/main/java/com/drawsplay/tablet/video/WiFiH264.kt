package com.drawsplay.tablet.video

import com.drawsplay.tablet.transport.WiFiSessionProtocol
import java.io.IOException
import java.nio.ByteBuffer

/** Progressive baseline AVC only. One AUD-delimited picture per complete Annex-B AU. */
class WiFiH264(private val config: Config) {
    data class Config(val width: Int, val height: Int, val fps: Int, val sps: ByteArray, val pps: ByteArray, val ppsId: Int,
        val frameNumBits: Int = 4, val pocType: Int = 2, val pocLsbBits: Int = 0)

    private val bits = Bits()

    /** Borrowed storage; validates slice boundaries without copying NALs or creating per-AU objects. */
    fun inspect(bytes: ByteArray, length: Int): Boolean {
        if (length !in 1..bytes.size) invalid()
        var offset = 0
        var pictures = 0
        var idr = false
        var previousMb = -1
        var frameNum = 0
        var idrId = 0
        var pocLsb = 0
        var reference = false
        var units = 0
        while (offset < length) {
            val prefix = prefix(bytes, offset, length)
            if (prefix == 0) invalid()
            val start = offset + prefix
            if (start >= length) invalid()
            var end = start + 1
            while (end < length && prefix(bytes, end, length) == 0) end++
            val header = bytes[start].toInt() and 255
            if (header and 128 != 0) invalid()
            val type = header and 31
            if (units++ == 0) {
                if (type != 9 || header != 9 || end - start != 2 ||
                    (bytes[start + 1].toInt() and 31) != 16 ||
                    (bytes[start + 1].toInt() and 255) ushr 5 > 1
                ) invalid()
            } else when (type) {
                1, 5 -> {
                    if (type == 5 && header and 96 == 0) invalid()
                    bits.reset(bytes, start + 1, end)
                    val firstMb = bits.ue(262143)
                    val sliceType = bits.ue(9)
                    if (sliceType != 0 && sliceType != 2 && sliceType != 5 && sliceType != 7) invalid()
                    if (type == 5 && sliceType != 2 && sliceType != 7) invalid()
                    if (bits.ue(255) != config.ppsId) invalid()
                    val sliceFrameNum = bits.read(config.frameNumBits)
                    val sliceIdrId = if (type == 5) bits.ue(65535) else 0
                    val slicePocLsb = if (config.pocType == 0) bits.read(config.pocLsbBits) else 0
                    val sliceReference = header and 96 != 0
                    if (pictures == 0) {
                        if (firstMb != 0) invalid()
                        idr = type == 5
                        frameNum = sliceFrameNum
                        idrId = sliceIdrId
                        pocLsb = slicePocLsb
                        reference = sliceReference
                    } else if ((type == 5) != idr || firstMb <= previousMb || sliceFrameNum != frameNum ||
                        sliceIdrId != idrId || slicePocLsb != pocLsb || sliceReference != reference
                    ) invalid()
                    previousMb = firstMb
                    pictures++
                }
                6 -> if (pictures != 0 || header != 6) invalid()
                else -> invalid()
            }
            offset = end
        }
        if (pictures == 0) invalid()
        return idr
    }

    private class Bits {
        private lateinit var data: ByteArray
        private var offset = 0
        private var end = 0
        private var remaining = 0
        private var current = 0
        private var zeros = 0

        fun reset(data: ByteArray, start: Int, end: Int) {
            this.data = data
            this.offset = start
            this.end = end
            remaining = 0
            zeros = 0
        }

        fun read(count: Int): Int {
            var result = 0
            repeat(count) {
                if (remaining == 0) {
                    if (offset >= end) invalid()
                    current = data[offset++].toInt() and 255
                    if (zeros >= 2 && current == 3) {
                        if (offset >= end || (data[offset].toInt() and 255) > 3) invalid()
                        current = data[offset++].toInt() and 255
                        zeros = 0
                    }
                    zeros = if (current == 0) zeros + 1 else 0
                    remaining = 8
                }
                result = (result shl 1) or ((current ushr --remaining) and 1)
            }
            return result
        }

        fun ue(max: Int): Int {
            var count = 0
            while (read(1) == 0) if (++count > 20) invalid()
            val value = (1 shl count) - 1 + read(count)
            if (value > max) invalid()
            return value
        }
    }

    companion object {
        /**
         * The negotiated Hello maxFps holds at maxWidth x maxHeight; smaller
         * sizes may run faster within the same macroblock-per-second budget
         * (never below maxFps, never above the Hello's 120 fps bound).
         */
        fun maxFpsAt(width: Int, height: Int, limits: WiFiSessionProtocol.Hello): Int {
            fun blocks(w: Int, h: Int) = ((w + 15) / 16) * ((h + 15) / 16)
            val session = blocks(width, height)
            if (session <= 0) return limits.maxFps
            val scaled = limits.maxFps.toLong() * blocks(limits.maxWidth, limits.maxHeight) / session
            return scaled.coerceIn(limits.maxFps.toLong(), 120L).toInt()
        }

        fun decodeConfig(raw: ByteArray, limits: WiFiSessionProtocol.Hello): Config {
            if (raw.size !in 16..65536) invalid()
            val data = ByteBuffer.wrap(raw)
            fun u16() = data.short.toInt() and 65535
            if (u16() != 1) invalid()
            val width = u16()
            val height = u16()
            val fps = u16()
            val spsLength = u16()
            val ppsLength = u16()
            if (data.int != 0 || width !in 320..limits.maxWidth || height !in 320..limits.maxHeight ||
                fps !in 1..maxFpsAt(width, height, limits) || spsLength !in 8..4096 || ppsLength !in 6..4096 ||
                raw.size != 16 + spsLength + ppsLength
            ) invalid()
            val sps = raw.copyOfRange(16, 16 + spsLength)
            val pps = raw.copyOfRange(16 + spsLength, raw.size)
            val spsPrefix = prefix(sps, 0, sps.size)
            val ppsPrefix = prefix(pps, 0, pps.size)
            if (spsPrefix == 0 || ppsPrefix == 0 || sps[spsPrefix] != 0x67.toByte() || pps[ppsPrefix] != 0x68.toByte()) invalid()
            for (index in spsPrefix + 1 until sps.size) if (prefix(sps, index, sps.size) != 0) invalid()
            for (index in ppsPrefix + 1 until pps.size) if (prefix(pps, index, pps.size) != 0) invalid()
            val bits = Bits()
            bits.reset(sps, spsPrefix + 1, sps.size)
            if (bits.read(8) != 66 || bits.read(8) and 3 != 0) invalid()
            val level = bits.read(8)
            if (level > limits.h264Level) invalid()
            val maxBlocks: Int
            val maxBlocksPerSecond: Int
            val maxDpbMbs: Int
            when (level) {
                31 -> { maxBlocks = 3600; maxBlocksPerSecond = 108000; maxDpbMbs = 18000 }
                32 -> { maxBlocks = 5120; maxBlocksPerSecond = 216000; maxDpbMbs = 20480 }
                40, 41 -> { maxBlocks = 8192; maxBlocksPerSecond = 245760; maxDpbMbs = 32768 }
                42 -> { maxBlocks = 8704; maxBlocksPerSecond = 522240; maxDpbMbs = 34816 }
                50 -> { maxBlocks = 22080; maxBlocksPerSecond = 589824; maxDpbMbs = 110400 }
                51 -> { maxBlocks = 36864; maxBlocksPerSecond = 983040; maxDpbMbs = 184320 }
                52 -> { maxBlocks = 36864; maxBlocksPerSecond = 2073600; maxDpbMbs = 184320 }
                else -> invalid()
            }
            val spsId = bits.ue(31)
            val frameNumBits = bits.ue(12) + 4
            val pocType = bits.ue(2)
            var pocLsbBits = 0
            when (pocType) {
                0 -> pocLsbBits = bits.ue(12) + 4
                2 -> Unit
                else -> invalid()
            }
            val maxRefFrames = bits.ue(16)
            if (bits.read(1) != 0) invalid() // No gaps in frame numbers.
            val widthMb = bits.ue(511) + 1
            val heightMb = bits.ue(511) + 1
            if (bits.read(1) != 1) invalid() // Progressive only.
            bits.read(1)
            var cropX = 0
            var cropY = 0
            if (bits.read(1) != 0) {
                cropX = bits.ue(8192) + bits.ue(8192)
                cropY = bits.ue(8192) + bits.ue(8192)
            }
            val blocks = widthMb * heightMb
            // Crop may only trim macroblock alignment padding, so the coded size stays within the negotiated maxima.
            if (widthMb * 16 - cropX * 2 != width || heightMb * 16 - cropY * 2 != height ||
                cropX * 2 >= 16 || cropY * 2 >= 16 ||
                widthMb * 16 > limits.maxWidth + 15 || heightMb * 16 > limits.maxHeight + 15 ||
                blocks > maxBlocks || blocks.toLong() * fps > maxBlocksPerSecond ||
                maxRefFrames > minOf(maxDpbMbs / blocks, 16)
            ) invalid()
            bits.reset(pps, ppsPrefix + 1, pps.size)
            val ppsId = bits.ue(255)
            if (bits.ue(31) != spsId || bits.read(1) != 0 || bits.read(1) != 0 || bits.ue(7) != 0) invalid()
            return Config(width, height, fps, sps, pps, ppsId, frameNumBits, pocType, pocLsbBits)
        }

        private fun prefix(data: ByteArray, offset: Int, end: Int): Int {
            if (offset + 3 > end || data[offset] != 0.toByte() || data[offset + 1] != 0.toByte()) return 0
            if (data[offset + 2] == 1.toByte()) return 3
            if (offset + 4 <= end && data[offset + 2] == 0.toByte() && data[offset + 3] == 1.toByte()) return 4
            return 0
        }

        private fun invalid(): Nothing = throw IOException("Unsupported or malformed Wi-Fi H.264 baseline config/access unit; reconnect with compatible mode")
    }
}
