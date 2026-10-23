package com.drawsplay.tablet.transport

import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.IOException
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.fail
import org.junit.Test

class WiFiControlLinkTest {
    private fun frame(type: Int, epoch: Long, sequence: Long, body: ByteArray): ByteArray {
        val out = ByteArrayOutputStream()
        val header = WiFiFrameCodec.Header().apply {
            this.type = type; this.epoch = epoch; this.sequence = sequence; payloadLength = body.size
        }
        WiFiFrameCodec.Writer(out).writeFrame(header, body, 0, body.size)
        return out.toByteArray()
    }

    @Test
    fun readsInputFramePayloadsAsOneByteStream() {
        val wire = frame(WiFiFrameCodec.INPUT, 9, 2, "abc".toByteArray()) + frame(WiFiFrameCodec.INPUT, 9, 3, "def".toByteArray())
        val link = WiFiControlLink(ByteArrayInputStream(wire), ByteArrayOutputStream(), 9)
        val buffer = ByteArray(6)
        var total = 0
        while (total < 6) total += link.inputStream.read(buffer, total, 6 - total)
        assertEquals("abcdef", String(buffer))
    }

    @Test
    fun rejectsWrongEpochStaleSequenceAndNonInputFrames() {
        val cases = listOf(
            frame(WiFiFrameCodec.INPUT, 8, 2, byteArrayOf(1)),
            frame(WiFiFrameCodec.INPUT, 9, 2, byteArrayOf(1)) + frame(WiFiFrameCodec.INPUT, 9, 2, byteArrayOf(1)),
            frame(WiFiFrameCodec.CONTROL, 9, 2, byteArrayOf(1))
        )
        for (wire in cases) {
            val link = WiFiControlLink(ByteArrayInputStream(wire), ByteArrayOutputStream(), 9)
            try {
                val sink = ByteArray(4)
                repeat(2) { link.inputStream.read(sink) }
                fail("accepted bad frame")
            } catch (expected: IOException) {
            }
        }
    }

    @Test
    fun writesBoundedInputFramesAndSharesSequenceWithControlRecords() {
        val out = ByteArrayOutputStream()
        val link = WiFiControlLink(ByteArrayInputStream(ByteArray(0)), out, 9)
        link.sendControl(byteArrayOf(7, 0, 0, 1))
        link.outputStream.write(ByteArray(WiFiFrameCodec.MAX_CONTROL_SIZE + 5) { 'x'.code.toByte() })
        val reader = WiFiFrameCodec.Reader(ByteArrayInputStream(out.toByteArray()))
        val body = ByteArray(WiFiFrameCodec.MAX_CONTROL_SIZE)
        val seen = mutableListOf<Triple<Int, Long, Int>>()
        repeat(3) {
            val h = reader.readHeader()
            seen += Triple(h.type, h.sequence, reader.readPayload(body))
            assertEquals(9L, h.epoch)
        }
        assertEquals(
            listOf(
                Triple(WiFiFrameCodec.CONTROL, 2L, 4),
                Triple(WiFiFrameCodec.INPUT, 3L, WiFiFrameCodec.MAX_CONTROL_SIZE),
                Triple(WiFiFrameCodec.INPUT, 4L, 5)
            ), seen
        )
    }

    @Test
    fun emptyWriteSendsNothing() {
        val out = ByteArrayOutputStream()
        WiFiControlLink(ByteArrayInputStream(ByteArray(0)), out, 9).outputStream.write(ByteArray(0))
        assertArrayEquals(ByteArray(0), out.toByteArray())
    }
}
