package com.drawsplay.tablet.transport

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class HostEndpointSelectorPairedTest {
    private val paired = java.net.Socket()

    @Test
    fun pairedSessionIsLastInputCandidateAndNeverAVideoCandidate() {
        val selector = HostEndpointSelector()
        val usbOnly = selector.inputCandidates()
        selector.updatePairedControl(paired)
        val input = selector.inputCandidates()
        assertEquals(usbOnly, input.dropLast(1))
        assertTrue(input.last().pairedSocket === paired)
        assertEquals(usbOnly, selector.videoCandidates())
    }

    @Test
    fun usbAndAccessoryCandidatesAreUnchangedWithoutAPairedSession() {
        val selector = HostEndpointSelector()
        selector.updateAccessoryAttached(true)
        val before = selector.inputCandidates()
        selector.updatePairedControl(paired)
        selector.updatePairedControl(null)
        assertEquals(before, selector.inputCandidates())
    }

    @Test
    fun closedPairedSocketIsNotOffered() {
        val selector = HostEndpointSelector()
        selector.updatePairedControl(paired)
        paired.close()
        assertTrue(selector.inputCandidates().none { it.pairedSocket != null })
    }
}
