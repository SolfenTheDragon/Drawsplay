package com.drawsplay.tablet.video

import org.junit.Assert.assertEquals
import org.junit.Test

class EchoTrackerTest {
    @Test
    fun reportsLatencyOncePerSequence() {
        val t0 = 1_000_000_000L
        val seq = EchoTracker.nextSeq(t0)
        EchoTracker.onFrameMeta(seq, t0 + 40_000_000)
        EchoTracker.onFrameMeta(seq, t0 + 90_000_000)
        assertEquals(40.0, EchoTracker.medianMs(t0 + 100_000_000), 0.001)
    }

    @Test
    fun ignoresUnknownSequence() {
        val now = 5_000_000_000L
        EchoTracker.onFrameMeta(0, now)
        EchoTracker.onFrameMeta(Int.MAX_VALUE, now)
    }
}
