package com.drawsplay.tablet.transport

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.PipedInputStream
import java.io.PipedOutputStream

class AccessoryFramingTest {
    @Test
    fun encodeMatchesHostLayout() {
        val frame = AccessoryFraming.encode(AccessoryFraming.TYPE_DATA, 0x0102, byteArrayOf(9, 8, 7))
        assertArrayEquals(byteArrayOf(3, 1, 2, 0, 3, 9, 8, 7), frame)
    }

    @Test
    fun helloValidation() {
        assertTrue(AccessoryFraming.isValidHello(AccessoryFraming.HELLO_PAYLOAD))
        assertFalse(AccessoryFraming.isValidHello(byteArrayOf(1, 2, 3)))
        val wrongVersion = AccessoryFraming.HELLO_PAYLOAD.copyOf().also { it[5] = 9 }
        assertFalse(AccessoryFraming.isValidHello(wrongVersion))
    }

    @Test
    fun parserReassemblesAnySplit() {
        val frames = listOf(
            AccessoryFraming.encode(AccessoryFraming.TYPE_OPEN, 1, byteArrayOf(2)),
            AccessoryFraming.encode(AccessoryFraming.TYPE_DATA, 1, ByteArray(AccessoryFraming.MAX_PAYLOAD) { it.toByte() }),
            AccessoryFraming.encode(AccessoryFraming.TYPE_CLOSE, 1, ByteArray(0))
        )
        val stream = frames.reduce { a, b -> a + b }
        for (chunk in listOf(1, 3, 7, 4096, stream.size)) {
            val got = mutableListOf<Triple<Int, Int, Int>>()
            val parser = AccessoryFrameParser { t, id, p -> got.add(Triple(t, id, p.size)) }
            var pos = 0
            while (pos < stream.size) {
                val n = minOf(chunk, stream.size - pos)
                parser.feed(stream.copyOfRange(pos, pos + n), n)
                pos += n
            }
            assertEquals(
                listOf(
                    Triple(AccessoryFraming.TYPE_OPEN, 1, 1),
                    Triple(AccessoryFraming.TYPE_DATA, 1, AccessoryFraming.MAX_PAYLOAD),
                    Triple(AccessoryFraming.TYPE_CLOSE, 1, 0)
                ),
                got
            )
        }
    }

    @Test(expected = IllegalStateException::class)
    fun parserRejectsOversizedFrame() {
        val parser = AccessoryFrameParser { _, _, _ -> }
        val bad = byteArrayOf(3, 0, 1, 0xFF.toByte(), 0xFF.toByte())
        parser.feed(bad, bad.size)
    }

    @Test
    fun linkHandshakeOpenAndData() {
        // Fake host: tablet writes into toHost, host writes into toTablet.
        val toTabletOut = PipedOutputStream()
        val toTabletIn = PipedInputStream(toTabletOut, 1 shl 16)
        val toHostOut = PipedOutputStream()
        val toHostIn = PipedInputStream(toHostOut, 1 shl 16)

        val link = AccessoryLink(toTabletIn, toHostOut) {}
        link.start()

        val hostFrames = java.util.concurrent.LinkedBlockingQueue<Triple<Int, Int, ByteArray>>()
        val hostParser = AccessoryFrameParser { t, id, p -> hostFrames.add(Triple(t, id, p)) }
        val hostReader = Thread {
            val buf = ByteArray(4096)
            try {
                while (true) {
                    val n = toHostIn.read(buf)
                    if (n < 0) break
                    hostParser.feed(buf, n)
                }
            } catch (_: Exception) {
            }
        }.apply { isDaemon = true; start() }

        val first = hostFrames.poll(2, java.util.concurrent.TimeUnit.SECONDS)!!
        assertEquals(AccessoryFraming.TYPE_HELLO, first.first)
        toTabletOut.write(AccessoryFraming.encode(AccessoryFraming.TYPE_HELLO, 0, AccessoryFraming.HELLO_PAYLOAD))
        toTabletOut.flush()

        val sock = link.open(AccessoryFraming.KIND_INPUT, 2000)
        var open: Triple<Int, Int, ByteArray>
        do {
            open = hostFrames.poll(2, java.util.concurrent.TimeUnit.SECONDS)!!
        } while (open.first == AccessoryFraming.TYPE_HELLO)
        assertEquals(AccessoryFraming.TYPE_OPEN, open.first)
        assertArrayEquals(byteArrayOf(AccessoryFraming.KIND_INPUT.toByte()), open.third)

        sock.getOutputStream().write("hi".toByteArray())
        val data = hostFrames.poll(2, java.util.concurrent.TimeUnit.SECONDS)!!
        assertEquals(AccessoryFraming.TYPE_DATA, data.first)
        assertEquals("hi", String(data.third))

        toTabletOut.write(AccessoryFraming.encode(AccessoryFraming.TYPE_DATA, open.second, "yo".toByteArray()))
        toTabletOut.flush()
        val buf = ByteArray(8)
        assertEquals(2, sock.getInputStream().read(buf))
        assertEquals("yo", String(buf, 0, 2))

        toTabletOut.write(AccessoryFraming.encode(AccessoryFraming.TYPE_CLOSE, open.second, ByteArray(0)))
        toTabletOut.flush()
        assertEquals(-1, sock.getInputStream().read(buf))

        link.close()
        hostReader.interrupt()
    }
}
