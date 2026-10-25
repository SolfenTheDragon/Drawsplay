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

    private class DelayedOutputDecoder(private val queue: WiFiAccessUnitQueue) : PlayerDecoder {
        private var onPictureReady: () -> Unit = {}
        override fun setOnPictureReadyListener(listener: () -> Unit) { onPictureReady = listener }
        var consumed = 0
        var outputs = 0
        override var pendingFrames = 0
        override val holdingRecoverySlot = false
        override val lastOutputUs = System.nanoTime() / 1000 - 10_000_000L
        override fun step(nowUs: Long): Boolean {
            if (pendingFrames > 0) {
                pendingFrames = 0
                outputs++
                onPictureReady()
                return true
            }
            val slot = queue.poll(nowUs) ?: return false
            queue.release(slot)
            pendingFrames = 1
            consumed++
            return true
        }
        override fun close() = Unit
    }

    @Test
    fun firstFrameAfterLongIdleIsNotAStallJustBecauseTheDecoderWasCreatedEarlier() {
        val idr = hex("000001091000000165b860")
        val wire = frame(5, 2, config()) + frame(6, 3, idr) + frame(WiFiFrameCodec.CONTROL, 4, byteArrayOf(5, 0, 0, 1))
        var decoder: DelayedOutputDecoder? = null
        val link = WiFiControlLink(ByteArrayInputStream(ByteArray(0)), ByteArrayOutputStream(), 42)
        WiFiVideoPlayer.run(ByteArrayInputStream(wire), {}, link, limits(), 42, { _, q -> DelayedOutputDecoder(q).also { decoder = it } }) { false }
        assertEquals(1, decoder!!.consumed)
        assertEquals("decoder must keep running after the first late frame", 1, decoder!!.outputs)
    }

    @Test
    fun pictureReadyIsNotSignalledByConfigurationOrConsumedInputWithoutOutput() {
        val idr = hex("000001091000000165b860")
        for (pictures in listOf(ByteArray(0), frame(6, 3, idr))) {
            val endSequence = if (pictures.isEmpty()) 3L else 4L
            val wire = frame(5, 2, config()) + pictures +
                frame(WiFiFrameCodec.CONTROL, endSequence, byteArrayOf(5, 0, 0, 1))
            var readyCount = 0
            val link = WiFiControlLink(ByteArrayInputStream(ByteArray(0)), ByteArrayOutputStream(), 42)
            WiFiVideoPlayer.run(ByteArrayInputStream(wire), {}, link, limits(), 42,
                { _, queue -> FakeDecoder(queue, mutableListOf()) },
                onPictureReady = { readyCount++ }) { false }
            assertEquals(0, readyCount)
        }
    }

    @Test
    fun pictureReadyIsSignalledOnceAfterPresentationAndResetsForNextRun() {
        val idr = hex("000001091000000165b860")
        val wire = frame(5, 2, config()) + frame(6, 3, idr) + frame(6, 4, idr) +
            frame(WiFiFrameCodec.CONTROL, 5, byteArrayOf(5, 0, 0, 1))
        var readyCount = 0
        repeat(2) {
            var decoder: DelayedOutputDecoder? = null
            val link = WiFiControlLink(ByteArrayInputStream(ByteArray(0)), ByteArrayOutputStream(), 42)
            WiFiVideoPlayer.run(ByteArrayInputStream(wire), {}, link, limits(), 42,
                { _, queue -> DelayedOutputDecoder(queue).also { decoder = it } },
                onPictureReady = {
                    assertTrue(decoder!!.outputs > 0)
                    readyCount++
                }) { false }
            assertEquals(it + 1, readyCount)
        }
    }

    @Test
    fun decoderCallbackAfterPlayerEndsCannotSignalPictureReady() {
        val wire = frame(5, 2, config()) + frame(WiFiFrameCodec.CONTROL, 3, byteArrayOf(5, 0, 0, 1))
        var callback: () -> Unit = {}
        var readyCount = 0
        val link = WiFiControlLink(ByteArrayInputStream(ByteArray(0)), ByteArrayOutputStream(), 42)
        WiFiVideoPlayer.run(ByteArrayInputStream(wire), {}, link, limits(), 42, { _, _ ->
            object : PlayerDecoder {
                override val pendingFrames = 0
                override val holdingRecoverySlot = false
                override val lastOutputUs = 0L
                override fun step(nowUs: Long) = false
                override fun setOnPictureReadyListener(listener: () -> Unit) { callback = listener }
                override fun close() = Unit
            }
        }, onPictureReady = { readyCount++ }) { false }
        callback()
        assertEquals(0, readyCount)
    }

    @Test
    fun decoderCallbackDuringCancellationCannotSignalPictureReady() {
        val wire = frame(5, 2, config()) + frame(WiFiFrameCodec.CONTROL, 3, byteArrayOf(5, 0, 0, 1))
        var cancelled = false
        var readyCount = 0
        val link = WiFiControlLink(ByteArrayInputStream(ByteArray(0)), ByteArrayOutputStream(), 42)
        WiFiVideoPlayer.run(ByteArrayInputStream(wire), {}, link, limits(), 42, { _, _ ->
            object : PlayerDecoder {
                private var callback: () -> Unit = {}
                override val pendingFrames = 0
                override val holdingRecoverySlot = false
                override val lastOutputUs = 0L
                override fun step(nowUs: Long) = false
                override fun setOnPictureReadyListener(listener: () -> Unit) { callback = listener }
                override fun close() {
                    cancelled = true
                    callback()
                }
            }
        }, onPictureReady = { readyCount++ }) { cancelled }
        assertEquals(0, readyCount)
    }

    @Test
    fun negotiatedStatsAreSentUpstreamOnTheVideoLinkWhileStreaming() {
        val media = java.io.PipedInputStream(1 shl 16)
        val feed = java.io.PipedOutputStream(media)
        val upstream = java.io.PipedOutputStream()
        val upstreamIn = java.io.PipedInputStream(upstream, 1 shl 16)
        val statsLink = WiFiControlLink(ByteArrayInputStream(ByteArray(0)), upstream, 42)
        val control = WiFiControlLink(ByteArrayInputStream(ByteArray(0)), ByteArrayOutputStream(), 42)
        val negotiated = limits().copy(features = WiFiSessionProtocol.FEATURE_LOW_LATENCY or WiFiSessionProtocol.FEATURE_VIDEO_STATS)
        val idr = hex("000001091000000165b860")
        feed.write(frame(5, 2, config()) + frame(6, 3, idr))
        feed.flush()
        val player = Thread {
            try {
                WiFiVideoPlayer.run(media, { feed.close() }, control, negotiated, 42, { _, queue -> FakeDecoder(queue, mutableListOf()) }, statsLink, 20L,
                    { WiFiVideoStats.Link(rssiDbm = -58, linkMbps = 390, frequencyMhz = 5745) }) { false }
            } catch (_: IOException) {
            }
        }
        player.start()
        val reader = WiFiFrameCodec.Reader(upstreamIn)
        val header = reader.readHeader()
        val payload = ByteArray(header.payloadLength)
        reader.readPayload(payload)
        assertEquals(WiFiFrameCodec.CONTROL, header.type)
        assertEquals(2L, header.sequence)
        assertArrayEquals(byteArrayOf(8, 0, 0, 1), payload.copyOf(4))
        val json = String(payload, 4, payload.size - 4, Charsets.UTF_8)
        assertTrue(json.contains("\"framesReceived\":1"))
        assertTrue(json, json.contains("\"wifiRssiDbm\":-58,\"wifiLinkMbps\":390,\"wifiFrequencyMhz\":5745"))
        feed.write(frame(WiFiFrameCodec.CONTROL, 4, byteArrayOf(5, 0, 0, 1)))
        feed.flush()
        player.join(5000)
        assertFalse(player.isAlive)
    }
}
