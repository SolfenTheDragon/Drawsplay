package com.drawsplay.tablet.transport

import java.io.DataInputStream
import java.io.IOException
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Assume.assumeTrue
import org.junit.Test

class WiFiEnrollmentTest {
    @Test(timeout = 30000)
    fun actualGoHostDialsTabletAndBindsNumericCodeToMutualTlsKeys() {
        val peer = System.getenv("DRAWSPLAY_PAIRING_TEST_PEER")
        assumeTrue("Configure DRAWSPLAY_PAIRING_TEST_PEER for Go/Kotlin network enrollment", !peer.isNullOrEmpty())
        val identity = testWiFiIdentity()
        for (correct in listOf(true, false)) {
            ServerSocket(0, 1, InetAddress.getByName("127.0.0.1")).use { listener ->
                listener.soTimeout = 5000
                val process = ProcessBuilder(peer!!, "--enrollment", "127.0.0.1:${listener.localPort}")
                    .redirectError(ProcessBuilder.Redirect.INHERIT).start()
                try {
                    val input = DataInputStream(process.inputStream)
                    val code = ByteArray(8).also { input.readFully(it) }.toString(Charsets.US_ASCII)
                    val entered = if (correct) code else if (code == "00000000") "11111111" else "00000000"
                    val raw = listener.accept()
                    try {
                        val pending = WiFiEnrollment(identity).authenticateAccepted(raw, entered, 5000)
                        pending.socket.use { socket ->
                            if (!correct) fail("incorrect code returned an authenticated connection")
                            assertTrue(pending.hostSpkiSha256.any { it != 0.toByte() })
                            socket.soTimeout = 3000
                            assertEquals(42, socket.inputStream.read())
                            socket.outputStream.write(43)
                        }
                    } catch (error: IOException) {
                        if (correct) throw error
                        assertTrue(raw.isClosed)
                    }
                    assertTrue(process.waitFor(5, TimeUnit.SECONDS))
                    assertEquals(if (correct) 0 else 1, process.exitValue())
                } finally {
                    process.destroyForcibly()
                }
            }
        }
    }

    @Test(timeout = 15000)
    fun wrongVersionTruncatedAndDribblingPeersFailClosed() {
        val identity = testWiFiIdentity()
        for (mode in listOf("version", "truncated", "dribble")) {
            ServerSocket(0, 1, InetAddress.getLoopbackAddress()).use { listener ->
                listener.soTimeout = 3000
                val executor = Executors.newSingleThreadExecutor()
                try {
                    val remote = executor.submit<Unit> {
                        Socket(InetAddress.getLoopbackAddress(), listener.localPort).use { raw ->
                            try {
                                when (mode) {
                                    "version" -> raw.outputStream.write(byteArrayOf(68, 83, 69, 80, 0, 3, 0, 0))
                                    "truncated" -> raw.outputStream.write(byteArrayOf(68, 83, 69))
                                    else -> repeat(40) {
                                        raw.outputStream.write(68)
                                        raw.outputStream.flush()
                                        Thread.sleep(40)
                                    }
                                }
                            } catch (expected: IOException) {
                                // The enrollment owner closes invalid or expired sockets.
                            }
                        }
                    }
                    val raw = listener.accept()
                    val start = System.nanoTime()
                    try {
                        WiFiEnrollment(identity).authenticateAccepted(raw, "01234567", 250).socket.use {
                            fail("malformed pre-authentication exchange returned a trusted socket")
                        }
                    } catch (expected: IOException) {
                        assertTrue(raw.isClosed)
                        assertTrue(TimeUnit.NANOSECONDS.toMillis(System.nanoTime() - start) < 2000)
                    }
                    remote.get(3, TimeUnit.SECONDS)
                } finally {
                    executor.shutdownNow()
                    assertTrue(executor.awaitTermination(3, TimeUnit.SECONDS))
                }
            }
        }
    }

}
