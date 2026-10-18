package com.drawsplay.tablet.video

import java.io.ByteArrayOutputStream
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.zip.Deflater
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class TileDecoderTest {
    private class Recorder : TileDecoder.Sink {
        var begin: Triple<Int, Int, Int>? = null
        var ended = false
        val tiles = ArrayList<Triple<Int, Int, IntArray>>()
        override fun onFullBegin(width: Int, height: Int, tileSize: Int) {
            begin = Triple(width, height, tileSize)
        }
        override fun onTile(tx: Int, ty: Int, w: Int, h: Int, argb: IntArray) {
            tiles.add(Triple(tx, ty, argb.copyOf(w * h)))
        }
        override fun onFullEnd() {
            ended = true
        }
    }

    private fun subFilter(rgb: ByteArray, w: Int, h: Int): ByteArray {
        val out = rgb.copyOf()
        for (row in 0 until h) {
            val base = row * w * 3
            for (i in w * 3 - 1 downTo 3) out[base + i] = (rgb[base + i] - rgb[base + i - 3]).toByte()
        }
        return out
    }

    private fun packet(flags: Int, width: Int, height: Int, tiles: List<Triple<Int, Int, ByteArray>>, tileSize: Int = 64): ByteArray {
        val raw = ByteArrayOutputStream()
        for ((tx, ty, rgb) in tiles) {
            val w = minOf(tileSize, width - tx * tileSize)
            val h = minOf(tileSize, height - ty * tileSize)
            raw.write(ByteBuffer.allocate(4).order(ByteOrder.LITTLE_ENDIAN).putShort(tx.toShort()).putShort(ty.toShort()).array())
            raw.write(subFilter(rgb, w, h))
        }
        val deflater = Deflater(1)
        deflater.setInput(raw.toByteArray())
        deflater.finish()
        val z = ByteArray(raw.size() + 64)
        val zn = deflater.deflate(z)
        deflater.end()
        val bb = ByteBuffer.allocate(TileDecoder.HEADER + zn).order(ByteOrder.LITTLE_ENDIAN)
        bb.put(1).put(flags.toByte()).putShort(width.toShort()).putShort(height.toShort())
            .putShort(tileSize.toShort()).putShort(tiles.size.toShort()).putInt(1).putInt(raw.size())
        bb.put(z, 0, zn)
        return bb.array()
    }

    @Test
    fun decodesFullRefreshWithEdgeTile() {
        val width = 70
        val height = 10
        val t0 = ByteArray(64 * 10 * 3) { (it * 7).toByte() }
        val t1 = ByteArray(6 * 10 * 3) { (it * 13 + 5).toByte() }
        val rec = Recorder()
        TileDecoder(rec).decode(packet(3, width, height, listOf(Triple(0, 0, t0), Triple(1, 0, t1))))
        assertEquals(Triple(70, 10, 64), rec.begin)
        assertTrue(rec.ended)
        assertEquals(2, rec.tiles.size)
        val px = rec.tiles[1].third
        assertEquals(6 * 10, px.size)
        val expected = (0xFF shl 24) or ((t1[0].toInt() and 0xFF) shl 16) or ((t1[1].toInt() and 0xFF) shl 8) or (t1[2].toInt() and 0xFF)
        assertEquals(expected, px[0])
        val last = 6 * 10 - 1
        val o = last * 3
        val expLast = (0xFF shl 24) or ((t1[o].toInt() and 0xFF) shl 16) or ((t1[o + 1].toInt() and 0xFF) shl 8) or (t1[o + 2].toInt() and 0xFF)
        assertEquals(expLast, px[last])
    }

    @Test
    fun deltaPacketHasNoBeginOrEnd() {
        val rec = Recorder()
        TileDecoder(rec).decode(packet(0, 64, 64, listOf(Triple(0, 0, ByteArray(64 * 64 * 3) { 9 }))))
        assertEquals(null, rec.begin)
        assertFalse(rec.ended)
        assertEquals(1, rec.tiles.size)
    }

    @Test(expected = IllegalArgumentException::class)
    fun rejectsCorruptStream() {
        val p = packet(3, 64, 64, listOf(Triple(0, 0, ByteArray(64 * 64 * 3) { 1 })))
        for (i in TileDecoder.HEADER until p.size) p[i] = 0x55
        TileDecoder(Recorder()).decode(p)
    }
}
