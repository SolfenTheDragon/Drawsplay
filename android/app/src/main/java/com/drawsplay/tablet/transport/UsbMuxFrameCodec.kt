package com.drawsplay.tablet.transport

import java.io.EOFException
import java.io.InputStream
import java.io.OutputStream
import java.nio.ByteBuffer
import java.nio.ByteOrder

/**
 * Fixed-header mux codec for the planned single-session USB transport.
 *
 * Header layout (big-endian):
 *  magic(2) version(1) stream(1) flags(1) seq(4) len(4)
 */
object UsbMuxFrameCodec {
    const val MAGIC: Short = 0x4453
    const val VERSION: Byte = 1
    const val HEADER_SIZE = 13
    const val MAX_PAYLOAD = 8 * 1024 * 1024

    const val STREAM_CONTROL: Byte = 1
    const val STREAM_INPUT: Byte = 2
    const val STREAM_VIDEO: Byte = 3
    const val STREAM_STATS: Byte = 4

    data class Frame(
        val streamId: Byte,
        val flags: Byte,
        val seq: UInt,
        val payload: ByteArray
    )

    fun write(out: OutputStream, frame: Frame) {
        writeFrame(out, frame)
        out.flush()
    }

    fun writeFrame(out: OutputStream, frame: Frame) {
        require(frame.payload.size <= MAX_PAYLOAD) { "payload too large: ${frame.payload.size}" }
        val header = ByteBuffer.allocate(HEADER_SIZE).order(ByteOrder.BIG_ENDIAN).apply {
            putShort(MAGIC)
            put(VERSION)
            put(frame.streamId)
            put(frame.flags)
            putInt(frame.seq.toInt())
            putInt(frame.payload.size)
        }.array()
        out.write(header)
        if (frame.payload.isNotEmpty()) {
            out.write(frame.payload)
        }
    }

    fun encode(frame: Frame): ByteArray {
        require(frame.payload.size <= MAX_PAYLOAD) { "payload too large: ${frame.payload.size}" }
        val header = ByteBuffer.allocate(HEADER_SIZE).order(ByteOrder.BIG_ENDIAN).apply {
            putShort(MAGIC)
            put(VERSION)
            put(frame.streamId)
            put(frame.flags)
            putInt(frame.seq.toInt())
            putInt(frame.payload.size)
        }.array()
        if (frame.payload.isEmpty()) return header
        return ByteArray(header.size + frame.payload.size).also {
            System.arraycopy(header, 0, it, 0, header.size)
            System.arraycopy(frame.payload, 0, it, header.size, frame.payload.size)
        }
    }

    fun read(input: InputStream): Frame {
        val header = readExactly(input, HEADER_SIZE)
        val bb = ByteBuffer.wrap(header).order(ByteOrder.BIG_ENDIAN)
        val magic = bb.short
        require(magic == MAGIC) { "invalid mux magic: 0x${magic.toString(16)}" }
        val version = bb.get()
        require(version == VERSION) { "unsupported mux version: $version" }
        val stream = bb.get()
        val flags = bb.get()
        val seq = bb.int.toUInt()
        val size = bb.int
        require(size in 0..MAX_PAYLOAD) { "invalid mux payload size: $size" }
        val payload = if (size == 0) ByteArray(0) else readExactly(input, size)
        return Frame(stream, flags, seq, payload)
    }

    private fun readExactly(input: InputStream, size: Int): ByteArray {
        val out = ByteArray(size)
        var off = 0
        while (off < size) {
            val n = input.read(out, off, size - off)
            if (n < 0) throw EOFException("unexpected EOF while reading $size bytes")
            off += n
        }
        return out
    }
}

