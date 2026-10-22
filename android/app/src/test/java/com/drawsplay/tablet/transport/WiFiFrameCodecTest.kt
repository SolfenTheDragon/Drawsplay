package com.drawsplay.tablet.transport

import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.EOFException
import java.io.IOException
import java.io.OutputStream
import java.util.Random
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Assert.fail
import org.junit.Test

class WiFiFrameCodecTest {
    private fun fixture(): ByteArray =
        "0300002400000003010203040506070800000009000000000000000a0000000000000000616263"
            .chunked(2).map { it.toInt(16).toByte() }.toByteArray()

    @Test
    fun matchesGoBigEndianFixtureAndBorrowsCallerPayloadStorage() {
        val source = ByteArrayInputStream(fixture())
        val reader = WiFiFrameCodec.Reader(source)
        val header = reader.readHeader()
        assertEquals(WiFiFrameCodec.INPUT, header.type)
        assertEquals(0x0102030405060708L, header.epoch)
        assertEquals(9L, header.sequence)
        assertEquals(10L, header.captureMonoUs)
        assertEquals(3, source.available())
        val payload = ByteArray(4096)
        assertEquals(3, reader.readPayload(payload))
        assertArrayEquals(byteArrayOf(97, 98, 99), payload.copyOf(3))
        val output = ByteArrayOutputStream()
        WiFiFrameCodec.Writer(output).writeFrame(header, payload, 0, 3)
        assertArrayEquals(fixture(), output.toByteArray())
    }

    @Test
    fun rejectsMalformedHeadersBeforePayloadRead() {
        for (offset in listOf(0, 1, 3, 4, 31, 35)) {
            val raw = fixture()
            raw[offset] = 127
            val source = ByteArrayInputStream(raw)
            try {
                WiFiFrameCodec.Reader(source).readHeader()
                fail("malformed header accepted at offset $offset")
            } catch (expected: IOException) {
                assertEquals(3, source.available())
            }
        }
    }

    @Test
    fun validatesHeaderBeforeBufferCheckoutAndEnforcesReadOrder() {
        val source = ByteArrayInputStream(fixture())
        val reader = WiFiFrameCodec.Reader(source)
        reader.readHeader()
        try {
            reader.readHeader()
            fail("pending payload was skipped")
        } catch (expected: IOException) {
            assertEquals(3, source.available())
        }
        try {
            reader.readPayload(ByteArray(2))
            fail("small payload buffer accepted")
        } catch (expected: IOException) {
            assertEquals(3, source.available())
        }
        assertEquals(3, reader.readPayload(ByteArray(3)))
        try {
            reader.readPayload(ByteArray(3))
            fail("payload read repeated")
        } catch (expected: IOException) {
            assertEquals(0, source.available())
        }
    }

    @Test(expected = EOFException::class)
    fun rejectsTruncatedPayload() {
        val raw = fixture()
        val reader = WiFiFrameCodec.Reader(ByteArrayInputStream(raw.copyOf(raw.size - 1)))
        reader.readHeader()
        reader.readPayload(ByteArray(4096))
    }

    @Test
    fun rejectsReplayWrongEpochAndSequenceWrapWithoutAdvancingOnRejection() {
        val guard = WiFiFrameCodec.SequenceGuard(7L)
        val header = WiFiFrameCodec.Header()
        header.epoch = 7L
        header.sequence = 1L
        guard.accept(header)
        header.sequence = 3L
        guard.accept(header)
        for ((epoch, sequence) in listOf(7L to 3L, 7L to 2L, 6L to 4L, 7L to 0L)) {
            header.epoch = epoch
            header.sequence = sequence
            try {
                guard.accept(header)
                fail("stale record accepted")
            } catch (expected: IOException) {
                // Rejected records must not consume a valid sequence.
            }
        }
        header.epoch = 7L
        header.sequence = 4L
        guard.accept(header)
        header.sequence = 0xffffffffL
        guard.accept(header)
        header.sequence = 1L
        try {
            guard.accept(header)
            fail("wrapped sequence accepted inside same epoch")
        } catch (expected: IOException) {
            // The full session, not just its counter, must be replaced.
        }
    }

    @Test
    fun successfulFrameOperationsAllocateNoObjectsAfterWarmup() {
        val raw = fixture()
        val source = ByteArrayInputStream(raw)
        val reader = WiFiFrameCodec.Reader(source)
        val payload = ByteArray(4096)
        val writer = WiFiFrameCodec.Writer(object : OutputStream() {
            override fun write(value: Int) = Unit
            override fun write(data: ByteArray, offset: Int, length: Int) = Unit
        })
        val original = reader.readHeader()
        reader.readPayload(payload)
        repeat(20000) {
            source.reset()
            val header = reader.readHeader()
            reader.readPayload(payload)
            writer.writeFrame(header, payload, 0, 3)
        }
        val factory = Class.forName("java.lang.management.ManagementFactory")
        val metrics = factory.getMethod("getThreadMXBean").invoke(null)
        val metricsType = Class.forName("com.sun.management.ThreadMXBean")
        metricsType.getMethod("setThreadAllocatedMemoryEnabled", java.lang.Boolean.TYPE)
            .invoke(metrics, true)
        val readAllocated = metricsType.getMethod("getThreadAllocatedBytes", java.lang.Long.TYPE)
        val thread = Thread.currentThread().id
        fun allocated(): Long = readAllocated.invoke(metrics, thread) as Long
        repeat(1000) { allocated() }
        val calibrationStart = allocated()
        val measurementOverhead = allocated() - calibrationStart
        val start = allocated()
        repeat(1000) {
            source.reset()
            val header = reader.readHeader()
            reader.readPayload(payload)
            writer.writeFrame(header, payload, 0, 3)
        }
        val measured = allocated() - start
        assertEquals(measurementOverhead, measured)
        source.reset()
        assertSame(original, reader.readHeader())
    }

    @Test
    fun malformedAndTruncatedEnvelopesStayBoundedUnderSeededMutation() {
        val random = Random(0x4453504cL)
        val payload = ByteArray(3)
        repeat(10000) {
            val raw = if (random.nextBoolean()) fixture() else {
                ByteArray(random.nextInt(80)).also { random.nextBytes(it) }
            }
            repeat(3) {
                if (raw.isNotEmpty()) raw[random.nextInt(raw.size)] = random.nextInt(256).toByte()
            }
            val source = ByteArrayInputStream(raw)
            val reader = WiFiFrameCodec.Reader(source)
            try {
                val header = reader.readHeader()
                assertEquals(raw.size - 36, source.available())
                val limit = when (header.type) {
                    WiFiFrameCodec.VIDEO_AU -> 1048576
                    WiFiFrameCodec.CONFIG -> 65536
                    else -> 4096
                }
                org.junit.Assert.assertTrue(header.payloadLength in 1..limit)
                reader.readPayload(payload)
            } catch (expected: IOException) {
                // Protocol failures are expected; unchecked crashes are not.
            }
        }
    }
}
