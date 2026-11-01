package com.drawsplay.tablet.video

import org.junit.Assert.assertEquals
import org.junit.Test

class FrameAckReporterTest {
    @Test
    fun ackLineIsOneJsonLineTheHostParses() {
        val line = FrameAckReporter.ackLine(1234)
        assertEquals('\n', line.last())
        assertEquals(1, line.count { it == '\n' })
        // Matches host/protocol.go clientStatsMsg: {"type":"ack","frames":N}.
        assertEquals("{\"type\":\"ack\",\"frames\":1234}\n", line)
    }
}
