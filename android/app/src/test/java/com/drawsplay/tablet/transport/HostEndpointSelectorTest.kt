package com.drawsplay.tablet.transport

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class HostEndpointSelectorTest {
    private val wifi = HostEndpoint(
        host = "192.168.50.10",
        inputPort = 37183,
        videoPort = 37184,
        authToken = "test-only-token",
        tls = true,
        tlsFingerprintSha256 = "ab".repeat(32)
    )

    @Test
    fun discoveredWifiEndpointRetainsPortsAndAuthenticationForBothChannels() {
        val selector = HostEndpointSelector()
        selector.updateWifiEndpoint(wifi)
        assertEquals(wifi, selector.inputCandidates().last())
        assertEquals(wifi, selector.videoCandidates().last())
    }

    @Test
    fun lostWifiEndpointIsRemovedFromBothChannels() {
        val selector = HostEndpointSelector()
        selector.updateWifiEndpoint(wifi)
        selector.updateWifiEndpoint(null)
        assertFalse(selector.inputCandidates().contains(wifi))
        assertFalse(selector.videoCandidates().contains(wifi))
    }

    @Test
    fun changedWifiEndpointReplacesStaleAddressRatherThanAccumulatingCandidates() {
        val selector = HostEndpointSelector()
        selector.updateWifiEndpoint(wifi)
        val changed = wifi.copy(host = "192.168.50.11")
        selector.updateWifiEndpoint(changed)
        assertFalse(selector.inputCandidates().contains(wifi))
        assertEquals(1, selector.inputCandidates().count { it == changed })
        assertTrue(selector.videoCandidates().contains(changed))
    }
}

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
