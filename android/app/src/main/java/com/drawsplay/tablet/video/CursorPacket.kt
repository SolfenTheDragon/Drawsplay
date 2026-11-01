package com.drawsplay.tablet.video

import java.nio.ByteBuffer
import java.nio.ByteOrder

/**
 * The host cursor, sent separately from the video (video packet type 5,
 * client feature `cursor1`) so cursor motion costs the host no encode and
 * never waits behind a video frame. The tablet draws it over the picture.
 *
 * Wire format (big-endian): u8 flags (1 = visible, 2 = shape follows),
 * i32 x, i32 y (top-left of the cursor image, host frame pixels), u32 shape
 * id; then, if a shape follows: u16 width, u16 height, u16 hotspot x,
 * u16 hotspot y and width*height*4 bytes of BGRA with straight alpha.
 */
data class CursorPacket(
    val visible: Boolean,
    val x: Int,
    val y: Int,
    val shapeId: Int,
    /** Present only when the shape changed. */
    val shape: Shape?
) {
    /** Pixels as Android ARGB_8888 colour ints (not premultiplied). */
    class Shape(val width: Int, val height: Int, val hotX: Int, val hotY: Int, val argb: IntArray)

    companion object {
        const val FLAG_VISIBLE = 1
        const val FLAG_SHAPE = 2
        private const val HEADER = 13
        private const val SHAPE_HEADER = 8
        private const val MAX_SIDE = 256

        /** Parses a cursor packet, or returns null when it is malformed. */
        fun parse(payload: ByteArray): CursorPacket? {
            if (payload.size < HEADER) return null
            val b = ByteBuffer.wrap(payload).order(ByteOrder.BIG_ENDIAN)
            val flags = b.get().toInt() and 0xFF
            val x = b.int
            val y = b.int
            val shapeId = b.int
            var shape: Shape? = null
            if (flags and FLAG_SHAPE != 0) {
                if (b.remaining() < SHAPE_HEADER) return null
                val w = b.short.toInt() and 0xFFFF
                val h = b.short.toInt() and 0xFFFF
                val hx = b.short.toInt() and 0xFFFF
                val hy = b.short.toInt() and 0xFFFF
                if (w == 0 || h == 0 || w > MAX_SIDE || h > MAX_SIDE) return null
                if (b.remaining() < w * h * 4) return null
                // BGRA bytes read little-endian are exactly 0xAARRGGBB.
                val pixels = IntArray(w * h)
                b.slice().order(ByteOrder.LITTLE_ENDIAN).asIntBuffer().get(pixels)
                shape = Shape(w, h, hx, hy, pixels)
            }
            return CursorPacket(flags and FLAG_VISIBLE != 0, x, y, shapeId, shape)
        }
    }
}
