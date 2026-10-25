package com.drawsplay.tablet

import org.junit.Assert.*
import org.junit.Test

class ConnectionPresentationTest {
    @Test fun configuredStreamStillWaitsForPicture() {
        assertEquals(ConnectionStatus.WAITING_PICTURE, ConnectionPresentation.status(
            configured = true, picture = false, savedWifi = true))
        assertEquals(ConnectionStatus.STREAMING, ConnectionPresentation.status(picture = true))
    }

    @Test fun usbPermissionHasPriorityOverWifiReconnect() {
        assertEquals(ConnectionStatus.USB_PERMISSION, ConnectionPresentation.status(
            savedWifi = true, usb = UsbConnectionStatus.PERMISSION_NEEDED))
        assertEquals(ConnectionStatus.USB_PRIORITY, ConnectionPresentation.status(
            savedWifi = true, usbBlocksWifi = true))
        assertEquals(ConnectionStatus.RECONNECTING_WIFI, ConnectionPresentation.status(savedWifi = true))
    }

    @Test fun repairIsNotMisrepresentedAsOrdinaryNetworkRetry() {
        assertEquals(ConnectionStatus.TRUST_REPAIR, ConnectionPresentation.status(
            savedWifi = true, wifi = PairedConnectionStatus.TRUST_REPAIR))
        assertEquals(ConnectionStatus.IDENTITY_REPAIR, ConnectionPresentation.status(
            savedWifi = true, wifi = PairedConnectionStatus.IDENTITY_REPAIR))
        assertEquals(ConnectionStatus.WIFI_NETWORK, ConnectionPresentation.status(
            savedWifi = true, wifi = PairedConnectionStatus.NETWORK_UNAVAILABLE))
    }

    @Test fun stoppedWifiDoesNotInventAUsbFailure() {
        assertEquals(ConnectionStatus.WAITING, ConnectionPresentation.status())
        assertEquals(ConnectionStatus.CONNECTING_USB, ConnectionPresentation.status(
            usb = UsbConnectionStatus.CONNECTED))
        assertEquals(ConnectionStatus.USB_ERROR, ConnectionPresentation.status(
            usb = UsbConnectionStatus.OPEN_FAILED))
    }

    @Test fun stalePairingCallbacksCannotReplaceRetryOrCancelledScreen() {
        val state = PairingAttemptState()
        val old = state.start()
        assertTrue(state.update(old, PairingStatus.EXPIRED))
        assertTrue(state.status.canRetry)
        val replacement = state.start()
        assertFalse(state.update(old, PairingStatus.PAIRED))
        assertEquals(PairingStatus.SEARCHING, state.status)
        assertTrue(state.update(replacement, PairingStatus.APPROVAL))
        assertFalse(state.status.canRetry)
        state.cancel()
        assertFalse(state.update(replacement, PairingStatus.PAIRED))
    }

    @Test fun failedPairingAllowsRetryButRequiresFreshCode() {
        for (error in listOf(PairingStatus.EXPIRED, PairingStatus.NOT_APPROVED,
            PairingStatus.VERIFICATION_FAILED, PairingStatus.NETWORK_UNAVAILABLE,
            PairingStatus.TRUST_REPAIR, PairingStatus.IDENTITY_REPAIR)) {
            val state = PairingAttemptState()
            val attempt = state.start()
            state.update(attempt, error)
            assertTrue(error.canRetry)
            state.start()
            assertEquals(PairingStatus.SEARCHING, state.status)
        }

    }

    @Test fun aFailureCannotBeOverwrittenByLateProgressFromTheSameAttempt() {
        val state = PairingAttemptState()
        val attempt = state.start()
        assertTrue(state.update(attempt, PairingStatus.DISCOVERY_FAILED))
        assertFalse(state.update(attempt, PairingStatus.APPROVAL))
        assertEquals(PairingStatus.DISCOVERY_FAILED, state.status)
        assertTrue(state.update(attempt, PairingStatus.DISCOVERY_FAILED))
    }
}
