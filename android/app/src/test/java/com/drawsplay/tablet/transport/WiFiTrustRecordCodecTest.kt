package com.drawsplay.tablet.transport

import java.io.IOException
import javax.crypto.KeyGenerator
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.fail
import org.junit.Test

class WiFiTrustRecordCodecTest {
    private fun key() = KeyGenerator.getInstance("AES").apply { init(256) }.generateKey()

    @Test
    fun authenticatedTrustRoundTripsAndUsesFreshNonces() {
        val key = key()
        val pin = ByteArray(32) { 5 }
        val first = WiFiTrustRecordCodec.seal(key, pin, "Desktop")
        val second = WiFiTrustRecordCodec.seal(key, pin, "Desktop")
        assertFalse(first.contentEquals(second))
        val opened = WiFiTrustRecordCodec.open(key, first)
        assertArrayEquals(pin, opened.spkiSha256)
        assertEquals("Desktop", opened.label)
    }

    @Test
    fun wrongKeysAndAnyModifiedRecordByteFailClosed() {
        val key = key()
        val encoded = WiFiTrustRecordCodec.seal(key, ByteArray(32) { 1 }, "Desktop")
        for (offset in encoded.indices) {
            val changed = encoded.copyOf().apply { this[offset] = (this[offset].toInt() xor 1).toByte() }
            try {
                WiFiTrustRecordCodec.open(key, changed)
                fail("changed trust record accepted at offset $offset")
            } catch (expected: IOException) {
            }
        }
        try {
            WiFiTrustRecordCodec.open(key(), encoded)
            fail("wrong Keystore key accepted")
        } catch (expected: IOException) {
        }
    }

    @Test
    fun invalidLabelsPinsAndOversizedRecordsDoNotBecomeTrust() {
        val key = key()
        for (label in listOf("", "x".repeat(64), "spoofed\nlog", "\uD800")) {
            try {
                WiFiTrustRecordCodec.seal(key, ByteArray(32) { 1 }, label)
                fail("invalid label accepted")
            } catch (expected: IOException) {
            }
        }
        for (pin in listOf(ByteArray(0), ByteArray(31), ByteArray(32), ByteArray(33))) {
            try {
                WiFiTrustRecordCodec.seal(key, pin, "Desktop")
                fail("invalid pin accepted")
            } catch (expected: IOException) {
            }
        }
        for (length in listOf(0, 1, 28, 8192)) {
            try {
                WiFiTrustRecordCodec.open(key, ByteArray(length))
                fail("invalid encrypted record accepted")
            } catch (expected: IOException) {
            }
        }
    }
}
