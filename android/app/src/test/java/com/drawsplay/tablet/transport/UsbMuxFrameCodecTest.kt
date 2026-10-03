package com.drawsplay.tablet.transport

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayOutputStream
import java.io.ByteArrayInputStream

class UsbMuxFrameCodecTest {

    @Test
    fun encodeAndReadRoundTrip() {
        val frame = UsbMuxFrameCodec.Frame(
            streamId = UsbMuxFrameCodec.STREAM_INPUT,
            flags = 0x3,
            seq = 42u,
            payload = byteArrayOf(1, 2, 3, 4)
        )
        val encoded = UsbMuxFrameCodec.encode(frame)
        val decoded = UsbMuxFrameCodec.read(ByteArrayInputStream(encoded))
        assertEquals(frame.streamId, decoded.streamId)
        assertEquals(frame.flags, decoded.flags)
        assertEquals(frame.seq, decoded.seq)
        assertArrayEquals(frame.payload, decoded.payload)
    }

    @Test
    fun rejectsOversizedPayload() {
        val tooLarge = ByteArray(UsbMuxFrameCodec.MAX_PAYLOAD + 1)
        try {
            UsbMuxFrameCodec.encode(
                UsbMuxFrameCodec.Frame(
                    streamId = UsbMuxFrameCodec.STREAM_CONTROL,
                    flags = 0,
                    seq = 1u,
                    payload = tooLarge
                )
            )
        } catch (ex: IllegalArgumentException) {
            assertTrue(ex.message?.contains("payload too large") == true)
            return
        }
        throw AssertionError("expected IllegalArgumentException for oversized payload")
    }

    @Test
    fun writeFrameMatchesEncodedBytes() {
        val frame = UsbMuxFrameCodec.Frame(
            streamId = UsbMuxFrameCodec.STREAM_VIDEO,
            flags = 0,
            seq = 9u,
            payload = byteArrayOf(10, 11, 12)
        )
        val out = ByteArrayOutputStream()
        UsbMuxFrameCodec.writeFrame(out, frame)
        assertArrayEquals(UsbMuxFrameCodec.encode(frame), out.toByteArray())
    }
}

