package com.drawsplay.tablet.transport

import java.io.IOException
import java.nio.ByteBuffer
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.fail
import org.junit.Test

class WiFiSessionProtocolTest {
    private fun fixture(): ByteArray = ByteArray(96).also {
        byteArrayOf(
            0, 2, 0, 2, 1, 0, 0, 1,
            7, -128, 4, -80, 0, 60, 0, 42,
            0, 16, 0, 0, 0, 64, 0, 0,
            0, 3, 0, 1, 0, 0, 0, 1
        ).copyInto(it)
        it.fill(7, 32, 64)
        it.fill(9, 64, 96)
    }

    @Test
    fun capabilitiesAreBoundToAuthenticatedRoleIdentityAndMemoryBounds() {
        val pin = ByteArray(32) { 7 }
        val valid = fixture()
        val decoded = WiFiSessionProtocol.decodeHello(valid, WiFiSessionProtocol.TABLET, pin)
        assertEquals(1920, decoded.maxWidth)
        assertEquals(1200, decoded.maxHeight)
        assertEquals(60, decoded.maxFps)
        assertEquals(3, decoded.queueSlots)
        val encoded = ByteArray(96)
        WiFiSessionProtocol.encodeHello(decoded, encoded)
        assertArrayEquals(valid, encoded)
        for (mode in listOf("truncated", "oversized", "old", "new", "role", "reserved", "pin", "nonce", "codec", "size", "fps", "au", "queue", "slots", "stats", "level")) {
            var raw = valid.copyOf()
            when (mode) {
                "truncated" -> raw = raw.copyOf(95)
                "oversized" -> raw = raw.copyOf(97)
                "old" -> raw[3] = 1
                "new" -> raw[1] = 3
                "role" -> raw[4] = 2
                "reserved" -> raw[5] = 1
                "pin" -> raw[32] = 6
                "nonce" -> raw.fill(0, 64)
                "codec" -> raw[7] = 0
                "size" -> { raw[8] = 0; raw[9] = 0 }
                "fps" -> raw[13] = 0
                "au" -> ByteBuffer.wrap(raw).putInt(16, 1024 * 1024 + 1)
                "queue" -> ByteBuffer.wrap(raw).putInt(20, 1024 * 1024)
                "slots" -> raw[25] = -1
                "stats" -> raw[27] = 2
                "level" -> raw[15] = -1
            }
            try {
                WiFiSessionProtocol.decodeHello(raw, WiFiSessionProtocol.TABLET, pin)
                fail("invalid $mode hello was accepted")
            } catch (expected: IOException) {
                // Refusal must happen before decoder/payload-pool allocation.
            }
        }
    }

    @Test
    fun negotiationUsesOnlyCommonCapabilitiesAndBoundedAllocation() {
        val tablet = WiFiSessionProtocol.decodeHello(fixture(), WiFiSessionProtocol.TABLET, ByteArray(32) { 7 })
        val host = tablet.copy(
            role = WiFiSessionProtocol.HOST, maxWidth = 1280, maxHeight = 720, maxFps = 30,
            maxAccessUnit = 256 * 1024, queueBytes = 512 * 1024, queueSlots = 2, features = 0
        )
        val negotiated = WiFiSessionProtocol.negotiate(host, tablet)
        assertEquals(1280, negotiated.maxWidth)
        assertEquals(720, negotiated.maxHeight)
        assertEquals(30, negotiated.maxFps)
        assertEquals(256 * 1024, negotiated.maxAccessUnit)
        assertEquals(512 * 1024, negotiated.queueBytes)
        assertEquals(2, negotiated.queueSlots)
        assertEquals(0L, negotiated.features)
        try {
            WiFiSessionProtocol.negotiate(tablet, host)
            fail("reversed roles were negotiated")
        } catch (expected: IOException) {
            // Role confusion cannot open a session.
        }
    }

    @Test
    fun negotiationKeepsVideoStatsOnlyWhenBothPeersOfferIt() {
        val tablet = WiFiSessionProtocol.decodeHello(fixture(), WiFiSessionProtocol.TABLET, ByteArray(32) { 7 })
            .copy(features = WiFiSessionProtocol.FEATURE_LOW_LATENCY or WiFiSessionProtocol.FEATURE_VIDEO_STATS)
        val host = tablet.copy(role = WiFiSessionProtocol.HOST)
        assertEquals(3L, WiFiSessionProtocol.negotiate(host, tablet).features)
        assertEquals(1L, WiFiSessionProtocol.negotiate(host.copy(features = 1L), tablet).features)
    }
}
