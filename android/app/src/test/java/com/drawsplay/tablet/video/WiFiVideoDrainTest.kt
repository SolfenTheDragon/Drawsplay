package com.drawsplay.tablet.video

import org.junit.Assert.*
import org.junit.Test

class WiFiVideoDrainTest {
    @Test
    fun heldRecoveryIdrKeepsDecoderBusyAfterEndEvenWithEmptyQueueAndNoPendingFrames() {
        assertFalse(WiFiVideoDecoder.drained(ended = true, queueDepth = 0, pendingFrames = 0, holdingRecoverySlot = true))
    }

    @Test
    fun drainedOnlyWhenEndedAndNothingQueuedPendingOrHeld() {
        assertTrue(WiFiVideoDecoder.drained(true, 0, 0, false))
        assertFalse(WiFiVideoDecoder.drained(false, 0, 0, false))
        assertFalse(WiFiVideoDecoder.drained(true, 1, 0, false))
        assertFalse(WiFiVideoDecoder.drained(true, 0, 1, false))
    }
}
