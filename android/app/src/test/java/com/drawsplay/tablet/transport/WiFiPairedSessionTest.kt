package com.drawsplay.tablet.transport

import java.io.DataInputStream
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.security.MessageDigest
import java.util.concurrent.TimeUnit
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Test

class WiFiPairedSessionTest {
    @Test(timeout = 30000)
    fun goHostNegotiatesCapabilitiesAndAssociatesTwoSeparatelyAuthenticatedChannels() {
        val peer = System.getenv("DRAWSPLAY_PAIRING_TEST_PEER")
        assumeTrue("Configure DRAWSPLAY_PAIRING_TEST_PEER for session integration", !peer.isNullOrEmpty())
        val identity = testWiFiIdentity()
        val alias = identity.getClientAliases("EC", null).single()
        val tabletPin = MessageDigest.getInstance("SHA-256").digest(identity.getCertificateChain(alias).first().publicKey.encoded)
        val hexPin = tabletPin.joinToString("") { "%02x".format(it.toInt() and 255) }
        ServerSocket(0, 1, InetAddress.getByName("127.0.0.1")).use { controlListener ->
            ServerSocket(0, 1, InetAddress.getByName("127.0.0.1")).use { videoListener ->
                controlListener.soTimeout = 5000
                videoListener.soTimeout = 5000
                val process = ProcessBuilder(
                    peer!!, "--session", "127.0.0.1:${controlListener.localPort}", "127.0.0.1:${videoListener.localPort}", hexPin
                ).redirectError(ProcessBuilder.Redirect.INHERIT).start()
                try {
                    val input = DataInputStream(process.inputStream)
                    val hostPin = ByteArray(32).also { input.readFully(it) }
                    val session = WiFiPairedSession(identity, hostPin)
                    val capabilities = WiFiSessionProtocol.Hello(
                        2, 2, WiFiSessionProtocol.TABLET, 1, 1280, 720, 30, 42,
                        256 * 1024, 512 * 1024, 2, 1, 1,
                        tabletPin, ByteArray(32) { 9 }
                    )
                    val control = session.acceptControl(controlListener.accept(), capabilities, 5000)
                    lateinit var video: Socket
                    control.use {
                        assertEquals(1280, control.limits.maxWidth)
                        assertEquals(720, control.limits.maxHeight)
                        assertEquals(30, control.limits.maxFps)
                        assertEquals(2, control.limits.queueSlots)
                        video = session.acceptVideo(videoListener.accept(), control, 5000)
                        video.soTimeout = 3000
                        val reader = WiFiFrameCodec.Reader(video.inputStream)
                        val header = reader.readHeader()
                        assertEquals(WiFiFrameCodec.CONFIG, header.type)
                        assertEquals(2L, header.sequence)
                        val message = ByteArray(4)
                        assertEquals(1, reader.readPayload(message))
                        assertEquals(42, message[0].toInt())
                        val controlReader = WiFiFrameCodec.Reader(control.socket.inputStream)
                        val ping = controlReader.readHeader()
                        assertEquals(WiFiFrameCodec.CONTROL, ping.type)
                        assertEquals(2L, ping.sequence)
                        assertEquals(4, controlReader.readPayload(message))
                        WiFiFrameCodec.Writer(control.socket.outputStream).writeFrame(
                            WiFiFrameCodec.Header().apply {
                                type = WiFiFrameCodec.CONTROL
                                epoch = ping.epoch
                                sequence = 2
                                payloadLength = 4
                            }, message, 0, 4
                        )
                        assertEquals(1, input.readUnsignedByte())
                        assertTrue(process.waitFor(5, TimeUnit.SECONDS))
                        assertEquals(0, process.exitValue())
                    }
                    assertTrue(control.socket.isClosed)
                    assertTrue(video.isClosed)
                } finally {
                    process.destroyForcibly()
                }
            }
        }
    }
}
