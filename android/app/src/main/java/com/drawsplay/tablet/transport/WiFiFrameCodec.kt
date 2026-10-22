package com.drawsplay.tablet.transport

import java.io.EOFException
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream

object WiFiFrameCodec {
    const val HEADER_SIZE = 36
    const val MAX_CONTROL_SIZE = 4096
    const val MAX_CONFIG_SIZE = 64 * 1024
    const val MAX_ACCESS_UNIT = 1024 * 1024
    const val HELLO = 1
    const val CONTROL = 2
    const val INPUT = 3
    const val ATTACH = 4
    const val CONFIG = 5
    const val VIDEO_AU = 6

    class Header {
        var type = 0
        var payloadLength = 0
        var epoch = 0L
        var sequence = 0L
        var captureMonoUs = 0L
        var durationUs = 0L
    }

    /** Single-owner reader; the returned header is borrowed until the next read. */
    class Reader(private val input: InputStream) {
        private val raw = ByteArray(HEADER_SIZE)
        private val header = Header()
        private var pending = 0

        fun readHeader(): Header {
            if (pending != 0) throw IOException("Wi-Fi payload must be consumed before the next header")
            readFully(input, raw, HEADER_SIZE)
            if (raw[1].toInt() != 0 || (raw[2].toInt() and 255) != 0 ||
                (raw[3].toInt() and 255) != HEADER_SIZE || u32(raw, 32) != 0L
            ) throw IOException("Invalid Wi-Fi envelope flags, size or reserved fields")
            header.type = raw[0].toInt() and 255
            header.payloadLength = u32(raw, 4).toInt()
            header.epoch = u64(raw, 8)
            header.sequence = u32(raw, 16)
            header.captureMonoUs = u64(raw, 20)
            header.durationUs = u32(raw, 28)
            validate(header)
            pending = header.payloadLength
            return header
        }

        /** Lengths are validated before callers need to lease any payload slot. */
        fun readPayload(destination: ByteArray): Int {
            if (pending == 0) throw IOException("Wi-Fi payload requires a validated header")
            if (pending > destination.size) throw IOException("Wi-Fi payload exceeds caller buffer")
            val length = pending
            readFully(input, destination, length)
            pending = 0
            return length
        }
    }

    /** Single-owner writer; neither header nor payload storage is copied. */
    class Writer(private val output: OutputStream) {
        private val raw = ByteArray(HEADER_SIZE)

        fun writeFrame(header: Header, payload: ByteArray, offset: Int, length: Int) {
            validate(header)
            if (offset < 0 || length != header.payloadLength || offset > payload.size ||
                length > payload.size - offset
            ) throw IOException("Wi-Fi payload length or buffer range mismatch")
            raw.fill(0)
            raw[0] = header.type.toByte()
            raw[3] = HEADER_SIZE.toByte()
            put(raw, 4, header.payloadLength.toLong(), 4)
            put(raw, 8, header.epoch, 8)
            put(raw, 16, header.sequence, 4)
            put(raw, 20, header.captureMonoUs, 8)
            put(raw, 28, header.durationUs, 4)
            output.write(raw, 0, raw.size)
            output.write(payload, offset, length)
        }
    }

    class SequenceGuard(private val epoch: Long) {
        private var last = 0L

        init {
            require(epoch != 0L) { "Wi-Fi session epoch must be nonzero" }
        }

        fun accept(header: Header) {
            if (header.epoch != epoch || header.sequence <= last ||
                header.sequence !in 1L..0xffffffffL
            ) throw IOException("Stale Wi-Fi epoch or sequence; reconnect before sequence wrap")
            last = header.sequence
        }
    }

    private fun validate(header: Header) {
        val limit = when (header.type) {
            HELLO -> {
                if (header.epoch != 0L || header.sequence != 0L) {
                    throw IOException("Wi-Fi bootstrap requires zero epoch and sequence")
                }
                MAX_CONTROL_SIZE
            }
            CONTROL, INPUT, ATTACH -> MAX_CONTROL_SIZE
            CONFIG -> MAX_CONFIG_SIZE
            VIDEO_AU -> MAX_ACCESS_UNIT
            else -> throw IOException("Unknown Wi-Fi envelope type")
        }
        if (header.type != HELLO && (header.epoch == 0L || header.sequence !in 1L..0xffffffffL)) {
            throw IOException("Wi-Fi session records require a nonzero epoch and sequence")
        }
        if (header.durationUs !in 0L..0xffffffffL ||
            (header.type != VIDEO_AU && header.durationUs != 0L)
        ) throw IOException("Invalid Wi-Fi record duration")
        if (header.payloadLength !in 1..limit) throw IOException("Wi-Fi payload exceeds record limit")
    }

    private fun readFully(input: InputStream, destination: ByteArray, length: Int) {
        var offset = 0
        while (offset < length) {
            val count = input.read(destination, offset, length - offset)
            if (count < 0) throw EOFException("Truncated Wi-Fi envelope or payload")
            if (count == 0) {
                val value = input.read()
                if (value < 0) throw EOFException("Truncated Wi-Fi envelope or payload")
                destination[offset++] = value.toByte()
            } else {
                offset += count
            }
        }
    }

    private fun u32(raw: ByteArray, offset: Int): Long {
        var value = 0L
        for (index in offset until offset + 4) {
            value = (value shl 8) or (raw[index].toLong() and 255L)
        }
        return value
    }

    private fun u64(raw: ByteArray, offset: Int): Long {
        var value = 0L
        for (index in offset until offset + 8) {
            value = (value shl 8) or (raw[index].toLong() and 255L)
        }
        return value
    }

    private fun put(raw: ByteArray, offset: Int, value: Long, bytes: Int) {
        for (index in 0 until bytes) {
            raw[offset + index] = (value ushr ((bytes - index - 1) * 8)).toByte()
        }
    }
}
