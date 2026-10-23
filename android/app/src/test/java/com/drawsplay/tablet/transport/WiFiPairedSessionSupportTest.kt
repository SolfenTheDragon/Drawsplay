package com.drawsplay.tablet.transport

import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.net.InetAddress
import org.junit.Assert.*
import org.junit.Test

class WiFiPairedSessionSupportTest {
    private fun ip(value: String) = InetAddress.getByName(value)

    @Test
    fun usbPresenceBlocksWifiButWifiAloneDoesNot() {
        assertFalse(UsbPriorityPolicy.allowWifi(accessoryAttached = true, usbPowered = false, adbEnabled = false))
        assertFalse(UsbPriorityPolicy.allowWifi(accessoryAttached = false, usbPowered = true, adbEnabled = true))
        assertTrue(UsbPriorityPolicy.allowWifi(accessoryAttached = false, usbPowered = true, adbEnabled = false))
        assertTrue(UsbPriorityPolicy.allowWifi(accessoryAttached = false, usbPowered = false, adbEnabled = true))
        assertTrue(UsbPriorityPolicy.allowWifi(accessoryAttached = false, usbPowered = false, adbEnabled = false))
    }

    @Test
    fun onlyDirectlyConnectedLanPeersAreAccepted() {
        val local = ip("192.168.1.50")
        assertTrue(WiFiLanPolicy.isDirectPeer(local, 24, ip("192.168.1.111")))
        assertFalse(WiFiLanPolicy.isDirectPeer(local, 24, ip("192.168.2.111")))
        assertFalse(WiFiLanPolicy.isDirectPeer(local, 24, ip("192.168.1.0")))
        assertFalse(WiFiLanPolicy.isDirectPeer(local, 24, ip("192.168.1.255")))
        assertFalse(WiFiLanPolicy.isDirectPeer(local, 24, local))
        assertFalse(WiFiLanPolicy.isDirectPeer(local, 31, ip("192.168.1.51")))
        assertFalse(WiFiLanPolicy.isDirectPeer(local, 24, ip("::1")))
    }

    @Test
    fun linkSocketCarriesInputOverTheControlLinkAndClosesOnce() {
        val wire = ByteArrayOutputStream()
        var closes = 0
        val link = WiFiControlLink(ByteArrayInputStream(ByteArray(0)), wire, 7)
        val socket = LinkSocket(link) { closes++ }
        socket.getOutputStream().write(byteArrayOf(1, 2, 3))
        assertTrue(wire.size() > 3)
        assertFalse(socket.isClosed)
        socket.close()
        socket.close()
        assertTrue(socket.isClosed)
        assertEquals(1, closes)
    }
}
