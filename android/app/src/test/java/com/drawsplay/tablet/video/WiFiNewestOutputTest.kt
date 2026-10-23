package com.drawsplay.tablet.video

import org.junit.Assert.*
import org.junit.Test

class WiFiNewestOutputTest {
    @Test
    fun aLateOutputWithNothingNewerIsStillShown() {
        val outputs = WiFiNewestOutput()
        assertEquals(-1, outputs.offer(4))
        assertEquals(4, outputs.takeNewest())
        assertEquals(-1, outputs.takeNewest())
    }

    @Test
    fun onlyOutputsSupersededInTheSameDrainAreDropped() {
        val outputs = WiFiNewestOutput()
        assertEquals(-1, outputs.offer(1))
        assertEquals(1, outputs.offer(2))
        assertEquals(2, outputs.offer(3))
        assertEquals(3, outputs.takeNewest())
    }
}
