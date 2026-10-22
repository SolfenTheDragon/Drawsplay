package com.drawsplay.tablet.transport

import java.io.DataInputStream
import java.util.concurrent.TimeUnit
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Test

class WiFiPairingInteropTest {
    @Test(timeout = 30000)
    fun productionGoServerAndKotlinClientConfirmOnlyMatchingCodes() {
        val peer = System.getenv("DRAWSPLAY_PAIRING_TEST_PEER")
        assumeTrue("Build cmd/wifi-pairing-test-peer and set DRAWSPLAY_PAIRING_TEST_PEER for cross-language test", !peer.isNullOrEmpty())
        for (code in listOf("01234567", "76543210")) {
            val process = ProcessBuilder(peer!!).redirectError(ProcessBuilder.Redirect.INHERIT).start()
            try {
                val input = DataInputStream(process.inputStream)
                assertEquals(0x44535032, input.readInt())
                fun bytes(length: Int): ByteArray = ByteArray(length).also { input.readFully(it) }
                val host = bytes(32)
                val tablet = bytes(32)
                val salt = bytes(16)
                val nonce = bytes(32)
                val serverPublic = bytes(384)
                WiFiPairingClient(code, host, tablet, salt, nonce, serverPublic).use { client ->
                    process.outputStream.write(client.publicValue())
                    process.outputStream.write(client.proof())
                    process.outputStream.flush()
                    val accepted = input.readBoolean()
                    if (code == "01234567") {
                        assertTrue(accepted)
                        assertTrue(client.verifyHost(bytes(32)))
                    } else {
                        assertFalse(accepted)
                        assertEquals(-1, input.read())
                    }
                }
                assertTrue("pairing peer did not exit", process.waitFor(5, TimeUnit.SECONDS))
                assertEquals(0, process.exitValue())
            } finally {
                process.destroyForcibly()
            }
        }
    }
}
