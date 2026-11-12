package com.drawsplay.tablet.protocol

import com.drawsplay.tablet.transport.AccessoryFrameParser
import com.drawsplay.tablet.transport.AccessoryFraming
import com.drawsplay.tablet.transport.UsbMuxFrameCodec
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Test
import java.io.ByteArrayInputStream

/** The app's framing against the same vectors the Go host is tested with. */
class SharedProtocolVectorsTest {
    @Test
    fun accessoryFramesMatchTheHost() {
        for (v in ProtocolVectors.load("aoa-frames.vectors")) {
            val type = v.long("type").toInt()
            val id = v.long("id").toInt()
            val payload = v.hex("payload")
            val wire = v.hex("wire")
            assertArrayEquals(v.name, wire, AccessoryFraming.encode(type, id, payload))
            val got = mutableListOf<Triple<Int, Int, ByteArray>>()
            AccessoryFrameParser { t, i, p -> got.add(Triple(t, i, p.copyOf())) }.feed(wire, wire.size)
            assertEquals(v.name, 1, got.size)
            assertEquals(v.name, type, got[0].first)
            assertEquals(v.name, id, got[0].second)
            assertArrayEquals(v.name, payload, got[0].third)
        }
    }

    @Test
    fun muxFramesMatchTheHost() {
        for (v in ProtocolVectors.load("mux-frames.vectors")) {
            val frame = UsbMuxFrameCodec.Frame(
                v.long("stream").toByte(), v.long("flags").toByte(), v.long("seq").toUInt(), v.hex("payload")
            )
            val wire = v.hex("wire")
            assertArrayEquals(v.name, wire, UsbMuxFrameCodec.encode(frame))
            val got = UsbMuxFrameCodec.read(ByteArrayInputStream(wire))
            assertEquals(v.name, frame.streamId, got.streamId)
            assertEquals(v.name, frame.flags, got.flags)
            assertEquals(v.name, frame.seq, got.seq)
            assertArrayEquals(v.name, frame.payload, got.payload)
        }
    }
}
