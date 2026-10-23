package com.drawsplay.tablet.video

import com.drawsplay.tablet.transport.WiFiControlLink
import com.drawsplay.tablet.transport.WiFiFrameCodec
import com.drawsplay.tablet.transport.WiFiSessionProtocol
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.nio.ByteBuffer
import java.util.concurrent.atomic.AtomicInteger
import org.junit.Assert.*
import org.junit.Test

class WiFiVideoPlayerTest {
    private fun limits() = WiFiSessionProtocol.Hello(
        2, 2, 2, 1, 1280, 720, 30, 31, 262144, 524288, 2, 1, 1,
        ByteArray(32) { 1 }, ByteArray(32) { 2 }
    )
    private fun hex(value: String) = value.chunked(2).map { it.toInt(16).toByte() }.toByteArray()
    private fun config(): ByteArray {
        val sps = hex("000000016742c01fda0280bfe5c044000003000400000300f23c60ca80")
        val pps = hex("0000000168ce0fc8")
        return ByteBuffer.allocate(16 + sps.size + pps.size).apply {
            putShort(1).putShort(640).putShort(360).putShort(30)
            putShort(sps.size.toShort()).putShort(pps.size.toShort()).putInt(0).put(sps).put(pps)
        }.array()
    }
    private fun frame(type: Int, sequence: Long, payload: ByteArray): ByteArray =
        ByteArrayOutputStream().also { output ->
            WiFiFrameCodec.Writer(output).writeFrame(WiFiFrameCodec.Header().apply {
                this.type = type
                this.sequence = sequence
                epoch = 42
                payloadLength = payload.size
                if (type == WiFiFrameCodec.VIDEO_AU) durationUs = 33333
            }, payload, 0, payload.size)
        }.toByteArray()

    private class FakeDecoder(private val queue: WiFiAccessUnitQueue, val order: MutableList<String>) : PlayerDecoder {
        val decoded = AtomicInteger()
        var closed = false
        override val pendingFrames = 0
        override val holdingRecoverySlot = false
        override val lastOutputUs = Long.MAX_VALUE
        override fun step(nowUs: Long): Boolean {
            val slot = queue.poll(nowUs) ?: return false
            queue.release(slot)
            decoded.incrementAndGet()
            return true
        }
        override fun close() { closed = true; order += "decoder-closed" }
    }

    @Test
    fun decoderReadyIsSentOnlyAfterDecoderExistsAndStreamDrainsToEnd() {
        val idr = hex("000001091000000165b860")
        val wire = frame(5, 2, config()) + frame(6, 3, idr) + frame(6, 4, idr) + frame(WiFiFrameCodec.CONTROL, 5, byteArrayOf(5, 0, 0, 1))
        val controlOut = ByteArrayOutputStream()
        val order = mutableListOf<String>()
        var decoder: FakeDecoder? = null
        val link = WiFiControlLink(ByteArrayInputStream(ByteArray(0)), object : java.io.OutputStream() {
            override fun write(b: Int) = controlOut.write(b)
            override fun write(b: ByteArray, o: Int, l: Int) { order += "ready-sent"; controlOut.write(b, o, l) }
        }, 42)
        WiFiVideoPlayer.run(ByteArrayInputStream(wire), {}, link, limits(), 42, { _, queue ->
            FakeDecoder(queue, order).also { decoder = it; order += "decoder-created" }
        }) { false }
        assertEquals(listOf("decoder-created", "ready-sent", "decoder-closed"), order.distinct())
        assertEquals(2, decoder!!.decoded.get())
        assertTrue(decoder!!.closed)
        val reader = WiFiFrameCodec.Reader(ByteArrayInputStream(controlOut.toByteArray()))
        val header = reader.readHeader()
        val payload = ByteArray(header.payloadLength)
        reader.readPayload(payload)
        assertArrayEquals(byteArrayOf(7, 0, 0, 1), payload.copyOf(4))
    }

    @Test
    fun corruptStreamClosesMediaAndSurfacesTheFailure() {
        val wire = frame(5, 2, config()) + frame(6, 9, hex("000001091000000165b860"))
        var mediaClosed = false
        val link = WiFiControlLink(ByteArrayInputStream(ByteArray(0)), ByteArrayOutputStream(), 42)
        val order = mutableListOf<String>()
        assertThrows(IOException::class.java) {
            WiFiVideoPlayer.run(ByteArrayInputStream(wire), { mediaClosed = true }, link, limits(), 42, { _, q -> FakeDecoder(q, order) }) { false }
        }
        assertTrue(mediaClosed)
        assertTrue(order.contains("decoder-closed"))
    }
}
