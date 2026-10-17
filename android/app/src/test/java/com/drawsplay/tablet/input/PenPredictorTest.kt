package com.drawsplay.tablet.input

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class PenPredictorTest {
    @Test
    fun needsTwoSamples() {
        val p = PenPredictor()
        assertNull(p.predict())
        p.record(0f, 0f, 0)
        assertNull(p.predict())
    }

    @Test
    fun extrapolatesAlongConstantVelocity() {
        val p = PenPredictor(lookaheadMs = 10f)
        p.record(0f, 0f, 0)
        p.record(4f, 2f, 4)
        val r = p.predict()
        assertNotNull(r)
        assertEquals(14f, r!!.x, 0.01f)
        assertEquals(7f, r.y, 0.01f)
    }

    @Test
    fun clampsDistance() {
        val p = PenPredictor(lookaheadMs = 100f, maxDistancePx = 10f)
        p.record(0f, 0f, 0)
        p.record(100f, 0f, 1)
        val r = p.predict()!!
        assertEquals(110f, r.x, 0.01f)
    }

    @Test
    fun noPredictionWhenStationaryOrReversing() {
        val slow = PenPredictor()
        slow.record(0f, 0f, 0)
        slow.record(0.01f, 0f, 8)
        assertNull(slow.predict())

        val turning = PenPredictor()
        turning.record(0f, 0f, 0)
        turning.record(10f, 0f, 4)
        turning.record(8f, 0f, 8)
        assertNull(turning.predict())
    }

    @Test
    fun ignoresStaleOrOutOfOrderAndLargeGaps() {
        val p = PenPredictor()
        p.record(0f, 0f, 10)
        p.record(5f, 0f, 10)
        assertNull(p.predict())
        p.record(5f, 0f, 200)
        assertNull(p.predict())
    }

    @Test
    fun resetForgetsHistory() {
        val p = PenPredictor()
        p.record(0f, 0f, 0)
        p.record(5f, 0f, 4)
        assertTrue(p.predict() != null)
        p.reset()
        assertNull(p.predict())
    }

    @Test
    fun clampsToView() {
        val c = PenPredictor.clampToView(PenPredictor.Point(-5f, 2000f), 100, 50)
        assertEquals(0f, c.x, 0f)
        assertEquals(49f, c.y, 0f)
    }
}
