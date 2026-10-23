package com.drawsplay.tablet.video

import com.drawsplay.tablet.transport.WiFiFrameCodec
import com.drawsplay.tablet.transport.WiFiSessionProtocol
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.nio.ByteBuffer
import org.junit.Assert.*
import org.junit.Test

class WiFiVideoReceiverTest {
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
    private fun frame(type: Int, sequence: Long, payload: ByteArray, epoch: Long = 42): ByteArray =
        ByteArrayOutputStream().also { output ->
            WiFiFrameCodec.Writer(output).writeFrame(WiFiFrameCodec.Header().apply {
                this.type = type
                this.sequence = sequence
                this.epoch = epoch
                payloadLength = payload.size
                if (type == WiFiFrameCodec.VIDEO_AU) durationUs = 33333
            }, payload, 0, payload.size)
        }.toByteArray()

    @Test
    fun authenticatedMediaUsesOnlyNegotiatedPoolAndDropsDependentPicturesOnOverflow() {
        val idr = hex("000001091000000165b860")
        val predicted = hex("0000010930000001419a18")
        val wire = frame(5, 2, config()) + frame(6, 3, idr) + frame(6, 4, predicted) +
            frame(6, 5, predicted) + frame(6, 6, idr)
        val receiver = WiFiVideoReceiver(ByteArrayInputStream(wire), limits(), 42) { 100 }
        assertEquals(640, receiver.config.width)
        assertEquals(524288, receiver.queue.storageBytes)
        receiver.receive()
        val held = receiver.queue.poll(100)!!
        receiver.receive()
        receiver.receive() // Overflow loses the queued reference, suppresses the next P picture.
        assertEquals(0, receiver.queue.depth)
        receiver.receive()
        receiver.queue.release(held)
        val fresh = receiver.queue.poll(100)!!
        assertTrue(fresh.restartDecoder)
        assertArrayEquals(idr, fresh.bytes.copyOf(fresh.length))
        receiver.queue.release(fresh)
        assertEquals(4L, receiver.received)
        assertEquals(2L, receiver.queue.dropped)
        assertThrows(IOException::class.java) { receiver.receive() }
    }

    @Test
    fun wrongEpochSequenceOversizedOrTruncatedAuCannotReachDecoder() {
        val idr = hex("000001091000000165b860")
        for (bad in listOf(
            frame(6, 3, idr, 43),
            frame(6, 2, idr),
            frame(6, 4, idr),
            frame(5, 3, config()),
            frame(6, 3, ByteArray(262145)),
            frame(6, 3, idr).dropLast(1).toByteArray()
        )) {
            val receiver = WiFiVideoReceiver(ByteArrayInputStream(frame(5, 2, config()) + bad), limits(), 42) { 100 }
            assertThrows(IOException::class.java) { receiver.receive() }
            assertEquals(0, receiver.queue.depth)
        }
        for (bad in listOf(frame(5, 3, config()), frame(5, 2, config(), 43), frame(6, 2, idr))) {
            assertThrows(IOException::class.java) {
                WiFiVideoReceiver(ByteArrayInputStream(bad), limits(), 42) { 100 }
            }
        }
    }

    @Test
    fun onlyExplicitBoundedEndRecordCompletesStreamNotTruncation() {
        val end = frame(2, 3, byteArrayOf(5, 0, 0, 1))
        val receiver = WiFiVideoReceiver(ByteArrayInputStream(frame(5, 2, config()) + end), limits(), 42) { 100 }
        assertFalse(receiver.receive())
        assertThrows(IOException::class.java) { receiver.receive() }
        val wrong = frame(2, 3, byteArrayOf(5, 0, 0, 2))
        val malformed = WiFiVideoReceiver(ByteArrayInputStream(frame(5, 2, config()) + wrong), limits(), 42) { 100 }
        assertThrows(IOException::class.java) { malformed.receive() }
    }

    @Test
    fun aPictureDelayedByTwoFrameIntervalsOfDecoderJitterIsStillDecoded() {
        // Discarding a queued picture costs a freeze until the next IDR, so a
        // short decoder hiccup must not age the queue out.
        val wire = frame(5, 2, config()) + frame(6, 3, hex("000001091000000165b860"))
        val receiver = WiFiVideoReceiver(ByteArrayInputStream(wire), limits(), 42) { 1_000_000 }
        receiver.receive()
        val slot = receiver.queue.poll(1_000_000 + 2 * 33_333 + 1)
        assertNotNull(slot)
        assertEquals(0L, receiver.queue.ageEvents)
    }

    @Test
    fun aQueueStuckBehindAStalledDecoderIsStillDiscarded() {
        val wire = frame(5, 2, config()) + frame(6, 3, hex("000001091000000165b860"))
        val receiver = WiFiVideoReceiver(ByteArrayInputStream(wire), limits(), 42) { 1_000_000 }
        receiver.receive()
        assertNull(receiver.queue.poll(1_000_000 + 250_000))
        assertEquals(1L, receiver.queue.ageEvents)
    }
}
