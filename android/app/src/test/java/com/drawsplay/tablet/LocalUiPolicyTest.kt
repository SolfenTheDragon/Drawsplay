package com.drawsplay.tablet

import org.junit.Assert.*
import org.junit.Test

class LocalUiPolicyTest {
    private val streaming = LocalUiPolicy(
        foreground = true, resumed = true, picture = true, waiting = false,
        pairing = false, dialog = false, windowFocused = true, streamingViewFocused = true
    )

    @Test fun onlyTheFocusedStreamingPadOwnsRemoteKeyboardInput() {
        assertTrue(streaming.remoteInput)
        val localStates = listOf(
            streaming.copy(picture = false),
            streaming.copy(waiting = true),
            streaming.copy(pairing = true),
            streaming.copy(dialog = true),
            streaming.copy(windowFocused = false),
            streaming.copy(streamingViewFocused = false),
            streaming.copy(foreground = false),
            streaming.copy(resumed = false)
        )
        localStates.forEach { assertFalse("Must keep keyboard local: $it", it.remoteInput) }
    }

    @Test fun waitingAndBackgroundReleasePerformanceRequests() {
        assertTrue(streaming.keepScreenOn)
        assertTrue(streaming.highRefresh)
        for (state in listOf(
            streaming.copy(picture = false, waiting = true),
            streaming.copy(foreground = false),
            streaming.copy(resumed = false)
        )) {
            assertFalse(state.keepScreenOn)
            assertFalse(state.highRefresh)
        }
    }

    @Test fun deliberatePairingStaysAwakeWithoutMaximumRefreshOrRemoteInput() {
        val pairing = streaming.copy(picture = false, waiting = true, pairing = true)
        assertTrue(pairing.keepScreenOn)
        assertFalse(pairing.highRefresh)
        assertFalse(pairing.remoteInput)
        assertFalse(pairing.copy(resumed = false).keepScreenOn)
        assertFalse(pairing.copy(pairing = false).keepScreenOn)
    }
}
