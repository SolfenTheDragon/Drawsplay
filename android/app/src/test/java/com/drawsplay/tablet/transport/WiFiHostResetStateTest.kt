package com.drawsplay.tablet.transport

import org.junit.Assert.*
import org.junit.Test

class WiFiHostResetStateTest {
    @Test
    fun pendingResetBlocksReconnectForReplacementActivity() {
        val processState = WiFiHostResetState()
        assertTrue(processState.begin())
        val replacementActivityState = processState
        assertTrue(replacementActivityState.reconnectBlocked)
        assertTrue(replacementActivityState.inProgress)
        assertFalse(replacementActivityState.begin())
    }

    @Test
    fun finishingResetKeepsOldHostBlockedButAllowsRetry() {
        val state = WiFiHostResetState()
        state.begin()
        state.finish()
        assertFalse(state.inProgress)
        assertTrue(state.reconnectBlocked)
        assertTrue(state.begin())
    }

    @Test
    fun onlyFreshPairingAfterResetCanEnableReconnect() {
        val state = WiFiHostResetState()
        val old = state.pairingEpoch()!!
        state.begin()
        assertNull(state.pairingEpoch())
        assertFalse(state.paired(old))
        assertTrue(state.reconnectBlocked)
        state.finish()
        assertFalse(state.paired(old))
        assertTrue(state.paired(state.pairingEpoch()!!))
        assertFalse(state.reconnectBlocked)
    }

    @Test
    fun successfulEnrollmentUnblocksReplacementActivityWithoutItsUiAttempt() {
        val processState = WiFiHostResetState()
        processState.begin()
        processState.finish()
        val epoch = processState.pairingEpoch()!!
        // The process-owned enrollment outcome does not depend on a UI generation.
        assertTrue(processState.paired(epoch))
        assertFalse(processState.reconnectBlocked)
    }

    @Test
    fun delayedSuccessCannotUndoASecondCompletedForget() {
        val state = WiFiHostResetState()
        val oldEpoch = state.pairingEpoch()!!
        state.begin()
        state.finish()
        assertFalse(state.paired(oldEpoch))
        assertTrue(state.reconnectBlocked)
    }
}
