package com.drawsplay.tablet.video

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.ByteArrayOutputStream
import java.io.DataOutputStream

class CursorPacketTest {
    // Builds a packet exactly as native/drawsplay-media-win (CursorRelay) does.
    private fun packet(visible: Boolean, x: Int, y: Int, id: Int, shape: Pair<Int, Int>? = null, bgra: ByteArray? = null): ByteArray {
        val bytes = ByteArrayOutputStream()
        DataOutputStream(bytes).apply {
            writeByte((if (visible) 1 else 0) or (if (shape != null) 2 else 0))
            writeInt(x)
            writeInt(y)
            writeInt(id)
            if (shape != null) {
                writeShort(shape.first)
                writeShort(shape.second)
                writeShort(3)
                writeShort(4)
                write(bgra!!)
            }
        }
        return bytes.toByteArray()
    }

    @Test
    fun parsesPositionOnlyUpdate() {
        val p = CursorPacket.parse(packet(true, -5, 1200, 77))
        assertNotNull(p)
        p!!
        assertTrue(p.visible)
        assertEquals(-5, p.x) // the image's top-left can be off-screen
        assertEquals(1200, p.y)
        assertEquals(77, p.shapeId)
        assertNull(p.shape)
    }

    @Test
    fun parsesShapeAsArgbInts() {
        // Two pixels, BGRA: opaque red, half-transparent blue.
        val bgra = byteArrayOf(0, 0, 0xFF.toByte(), 0xFF.toByte(), 0xFF.toByte(), 0, 0, 0x80.toByte())
        val p = CursorPacket.parse(packet(true, 1, 2, 9, 2 to 1, bgra))!!
        val s = p.shape!!
        assertEquals(2, s.width)
        assertEquals(1, s.height)
        assertEquals(3, s.hotX)
        assertEquals(4, s.hotY)
        assertArrayEquals(intArrayOf(0xFFFF0000.toInt(), 0x800000FF.toInt()), s.argb)
    }

    @Test
    fun hiddenCursor() {
        assertFalse(CursorPacket.parse(packet(false, 0, 0, 1))!!.visible)
    }

    @Test
    fun rejectsTruncatedOrOversizedShapes() {
        assertNull(CursorPacket.parse(ByteArray(5)))
        val full = packet(true, 0, 0, 1, 2 to 2, ByteArray(16))
        assertNull(CursorPacket.parse(full.copyOf(full.size - 1)))
        assertNull(CursorPacket.parse(packet(true, 0, 0, 1, 1000 to 1, ByteArray(4000))))
    }
}
