package com.drawsplay.tablet.transport

import java.io.IOException
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.fail
import org.junit.Test

class WiFiSessionOfferTest {
    @Test
    fun offerAndVideoAttachPreserveCredentialsAndRefuseMalformedRecords() {
        val raw = ByteArray(56).apply {
            this[0] = 1; this[3] = 1
            fill(3, 4, 20)
            fill(5, 20, 52)
            byteArrayOf(0, 0, 19, -120).copyInto(this, 52)
        }
        val offer = WiFiSessionProtocol.decodeOffer(raw, 99)
        assertEquals(99L, offer.epoch)
        assertArrayEquals(ByteArray(16) { 3 }, offer.id)
        assertArrayEquals(ByteArray(32) { 5 }, offer.ticket)
        val attach = ByteArray(52)
        WiFiSessionProtocol.encodeAttach(offer, attach)
        assertArrayEquals(byteArrayOf(2, 0, 0, 1), attach.copyOfRange(0, 4))
        assertArrayEquals(raw.copyOfRange(4, 52), attach.copyOfRange(4, 52))
        for (mode in listOf("length", "kind", "reserved", "schema", "id", "ticket", "ttl", "epoch")) {
            var bad = raw.copyOf()
            var epoch = 99L
            when (mode) {
                "length" -> bad = bad.copyOf(55)
                "kind" -> bad[0] = 2
                "reserved" -> bad[1] = 1
                "schema" -> bad[3] = 2
                "id" -> bad.fill(0, 4, 20)
                "ticket" -> bad.fill(0, 20, 52)
                "ttl" -> bad[55] = 0
                "epoch" -> epoch = 0
            }
            try {
                WiFiSessionProtocol.decodeOffer(bad, epoch)
                fail("invalid $mode session offer was accepted")
            } catch (expected: IOException) {
                // Never allocate/admit video from a malformed control offer.
            }
        }
    }
}
