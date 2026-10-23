package com.drawsplay.tablet.transport

import java.io.IOException
import java.io.InputStream
import java.io.OutputStream

/**
 * Presents a paired Wi-Fi control socket as the plain byte stream InputChannel
 * already speaks. Host-to-tablet bytes arrive in INPUT frames; tablet-to-host
 * bytes leave in INPUT frames of at most 4096 bytes. [sendControl] shares the
 * tablet's outgoing sequence (first record is sequence 2, after the host offer).
 */
class WiFiControlLink(input: InputStream, private val output: OutputStream, private val epoch: Long) {
    private val reader = WiFiFrameCodec.Reader(input)
    private val guard = WiFiFrameCodec.SequenceGuard(epoch)
    private val writer = WiFiFrameCodec.Writer(output)
    private val header = WiFiFrameCodec.Header()
    private val writeLock = Any()
    private var sequence = 2L
    private val readBuffer = ByteArray(WiFiFrameCodec.MAX_CONTROL_SIZE)
    private var readOffset = 0
    private var readLimit = 0

    val inputStream: InputStream = object : InputStream() {
        override fun read(): Int {
            val one = ByteArray(1)
            return if (read(one, 0, 1) < 0) -1 else one[0].toInt() and 255
        }

        override fun read(destination: ByteArray, offset: Int, length: Int): Int {
            if (length == 0) return 0
            while (readOffset == readLimit) {
                val frame = reader.readHeader()
                if (frame.type != WiFiFrameCodec.INPUT) throw IOException("Unexpected Wi-Fi control frame type")
                guard.accept(frame)
                readLimit = reader.readPayload(readBuffer)
                readOffset = 0
            }
            val count = minOf(length, readLimit - readOffset)
            System.arraycopy(readBuffer, readOffset, destination, offset, count)
            readOffset += count
            return count
        }
    }

    val outputStream: OutputStream = object : OutputStream() {
        override fun write(value: Int) = write(byteArrayOf(value.toByte()), 0, 1)

        override fun write(source: ByteArray, offset: Int, length: Int) {
            var position = offset
            val end = offset + length
            while (position < end) {
                val chunk = minOf(WiFiFrameCodec.MAX_CONTROL_SIZE, end - position)
                send(WiFiFrameCodec.INPUT, source, position, chunk)
                position += chunk
            }
        }

        override fun flush() = output.flush()
    }

    /** One control record such as decoder-ready; never interleaves inside another frame. */
    fun sendControl(payload: ByteArray) = send(WiFiFrameCodec.CONTROL, payload, 0, payload.size)

    private fun send(type: Int, payload: ByteArray, offset: Int, length: Int) {
        synchronized(writeLock) {
            if (sequence > 0xffffffffL) throw IOException("Wi-Fi control sequence exhausted; reconnect")
            header.type = type
            header.epoch = epoch
            header.sequence = sequence
            header.payloadLength = length
            writer.writeFrame(header, payload, offset, length)
            output.flush()
            sequence++
        }
    }
}
