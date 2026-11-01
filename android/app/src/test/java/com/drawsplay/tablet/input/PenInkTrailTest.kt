package com.drawsplay.tablet.input

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class PenInkTrailTest {
    private fun pt(x: Float, tMs: Long) = PenInkTrail.Point(x, 0f, 1f, tMs * 1_000_000)

    @Test
    fun reflectedFrameErasesShownPartAndKeepsAnchor() {
        val ink = PenInkTrail()
        ink.begin(pt(0f, 0))
        ink.add(pt(1f, 10))
        ink.add(pt(2f, 20))
        ink.add(pt(3f, 30))
        // The host's frame covers input sent at 55 ms; minus the 35 ms grace
        // for the host app's drawing lag, points up to 20 ms are shown.
        assertTrue(ink.eraseReflected(55_000_000))
        val s = ink.strokes.single()
        assertEquals(listOf(3f), s.points.map { it.x })
        // The preview continues from the newest point the host already shows.
        assertEquals(2f, s.anchor!!.x)
    }

    @Test
    fun finishedStrokeDisappearsOnceFullyShown() {
        val ink = PenInkTrail()
        ink.begin(pt(0f, 0))
        ink.add(pt(1f, 5))
        ink.end()
        ink.eraseReflected(100_000_000)
        assertTrue(ink.isEmpty)
    }

    @Test
    fun openStrokeKeepsAnchorWhenFullyShown() {
        val ink = PenInkTrail()
        ink.begin(pt(0f, 0))
        ink.add(pt(1f, 5))
        ink.eraseReflected(100_000_000)
        val s = ink.strokes.single()
        assertTrue(s.points.isEmpty())
        assertEquals(1f, s.anchor!!.x)
        // New points continue from the anchor.
        ink.add(pt(2f, 200))
        assertEquals(1, ink.strokes.size)
    }

    @Test
    fun ageFallbackWithoutFrameMetadata() {
        val ink = PenInkTrail()
        ink.begin(pt(0f, 0))
        ink.add(pt(1f, 100))
        ink.end()
        assertTrue(ink.eraseOlderThan(200_000_000, PenInkTrail.UNREFLECTED_MAX_AGE_NS))
        assertEquals(listOf(1f), ink.strokes.single().points.map { it.x })
        assertFalse(ink.eraseOlderThan(200_000_000, PenInkTrail.UNREFLECTED_MAX_AGE_NS))
    }
}
