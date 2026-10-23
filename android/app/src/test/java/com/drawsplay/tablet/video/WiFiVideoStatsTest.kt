package com.drawsplay.tablet.video

import org.junit.Assert.*
import org.junit.Test

class WiFiVideoStatsTest {
    @Test
    fun recordsCarryPerIntervalDeltasInTheUsbStatsSchema() {
        val stats = WiFiVideoStats()
        stats.next(WiFiVideoStats.Counters(received = 10, rendered = 9, decodeDrops = 1, queueDropped = 0, queued = 0), 500, true)
        val raw = stats.next(WiFiVideoStats.Counters(received = 40, rendered = 35, decodeDrops = 3, queueDropped = 2, queued = 2), 500, false)
        assertArrayEquals(byteArrayOf(8, 0, 0, 1), raw.copyOf(4))
        assertEquals(
            "{\"type\":\"stats\",\"intervalMs\":500,\"framesReceived\":30,\"framesRendered\":26," +
                "\"avgDecodeMs\":0,\"queuedFrames\":2,\"frameDrops\":4,\"staleFrames\":2,\"appForeground\":false}",
            String(raw, 4, raw.size - 4, Charsets.UTF_8)
        )
    }

    @Test
    fun eachNewQueueDiscardAsksForOneKeyframe() {
        val stats = WiFiVideoStats()
        assertFalse(stats.keyframeNeeded(0))
        assertTrue(stats.keyframeNeeded(1))
        assertFalse(stats.keyframeNeeded(1))
        assertTrue(stats.keyframeNeeded(3))
        assertArrayEquals(byteArrayOf(9, 0, 0, 1), WiFiVideoStats.KEYFRAME_REQUEST)
    }
}
