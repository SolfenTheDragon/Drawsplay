package com.drawsplay.tablet.transport

import java.io.File
import java.io.IOException
import java.io.DataInputStream
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.net.SocketTimeoutException
import java.nio.file.Files
import java.security.KeyStore
import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import javax.net.ssl.KeyManager
import javax.net.ssl.KeyManagerFactory
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLSocket
import javax.net.ssl.X509TrustManager
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

class WiFiMutualTlsTest {
    private data class Identity(val managers: Array<KeyManager>, val pin: ByteArray)

    @Test
    fun stalledAndSlowDribbleHandshakesCannotRenewTheTotalDeadline() {
        val tablet = identity()
        for (dribble in listOf(false, true)) {
            ServerSocket(0, 1, InetAddress.getLoopbackAddress()).use { listener ->
                listener.soTimeout = 4000
                val executor = Executors.newSingleThreadExecutor()
                try {
                    val peer = executor.submit<Unit> {
                        Socket(InetAddress.getLoopbackAddress(), listener.localPort).use { socket ->
                            socket.soTimeout = 4000
                            val input = DataInputStream(socket.inputStream)
                            input.readUnsignedByte()
                            input.readUnsignedShort()
                            val hello = ByteArray(input.readUnsignedShort())
                            input.readFully(hello)
                            if (dribble) {
                                try {
                                    socket.outputStream.write(byteArrayOf(22, 3, 3, 0, 100))
                                    repeat(100) {
                                        socket.outputStream.write(0)
                                        socket.outputStream.flush()
                                        Thread.sleep(40)
                                    }
                                } catch (expected: IOException) {
                                    // The absolute deadline closes this slow peer.
                                }
                            } else {
                                while (input.read() != -1) {
                                    // Never answer ClientHello.
                                }
                            }
                        }
                    }
                    val raw = listener.accept()
                    val start = System.nanoTime()
                    try {
                        WiFiMutualTls(tablet.managers, ByteArray(32) { 1 }).authenticateAccepted(raw, 250).use {
                            fail("incomplete handshake returned an authenticated socket")
                        }
                    } catch (expected: SocketTimeoutException) {
                        assertTrue(raw.isClosed)
                        assertTrue(TimeUnit.NANOSECONDS.toMillis(System.nanoTime() - start) < 2000)
                    }
                    peer.get(5, TimeUnit.SECONDS)
                } finally {
                    executor.shutdownNow()
                    assertTrue(executor.awaitTermination(5, TimeUnit.SECONDS))
                }
            }
        }
    }

    private fun identity(): Identity {
        val directory = Files.createTempDirectory("drawsplay-mutual-tls-test").toFile()
        try {
            val keyFile = File(directory, "identity.p12")
            val log = File(directory, "keytool.log")
            val process = ProcessBuilder(
                File(System.getProperty("java.home"), "bin/keytool").absolutePath,
                "-genkeypair", "-alias", "test", "-keyalg", "EC", "-groupname", "secp256r1",
                "-dname", "CN=WiFi test", "-validity", "1", "-storetype", "PKCS12",
                "-keystore", keyFile.absolutePath, "-storepass", "public-test-only", "-noprompt"
            ).redirectErrorStream(true).redirectOutput(log).start()
            try {
                assertTrue(process.waitFor(15, TimeUnit.SECONDS))
                assertEquals(log.readText(), 0, process.exitValue())
            } finally {
                process.destroyForcibly()
            }
            val store = KeyStore.getInstance("PKCS12")
            keyFile.inputStream().use { store.load(it, "public-test-only".toCharArray()) }
            val managers = KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm()).apply {
                init(store, "public-test-only".toCharArray())
            }.keyManagers
            return Identity(managers, MessageDigest.getInstance("SHA-256").digest(store.getCertificate("test").publicKey.encoded))
        } finally {
            check(directory.deleteRecursively()) { "Temporary TLS identity cleanup failed" }
        }
    }

    @Test
    fun hostInitiatedTlsRequiresBothPinnedIdentitiesAndNegotiatesModernProtocols() {
        val host = identity()
        val tablet = identity()
        for (protocol in listOf("TLSv1.2", "TLSv1.3")) {
            for (wrongPin in listOf(false, true)) {
                ServerSocket(0, 1, InetAddress.getLoopbackAddress()).use { listener ->
                    listener.soTimeout = 5000
                    val executor = Executors.newSingleThreadExecutor()
                    try {
                        val hostTask = executor.submit<Boolean> {
                            val trust = object : X509TrustManager {
                                override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
                                override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) =
                                    throw CertificateException("Not a client context")
                                override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) {
                                    val certificate = chain?.firstOrNull() ?: throw CertificateException("Missing tablet identity")
                                    if (!MessageDigest.isEqual(tablet.pin, MessageDigest.getInstance("SHA-256").digest(certificate.publicKey.encoded))) {
                                        throw CertificateException("Unexpected tablet")
                                    }
                                }

                            }
                            val context = SSLContext.getInstance("TLS")
                            context.init(host.managers, arrayOf(trust), null)
                            Socket(InetAddress.getLoopbackAddress(), listener.localPort).use { raw ->
                                (context.socketFactory.createSocket(raw, "localhost", listener.localPort, true) as SSLSocket).use { socket ->
                                    socket.useClientMode = false
                                    socket.needClientAuth = true
                                    socket.enabledProtocols = arrayOf(protocol)
                                    socket.soTimeout = 4000
                                    try {
                                        socket.startHandshake()
                                        socket.outputStream.write(42)
                                        assertEquals(43, socket.inputStream.read())
                                        true
                                    } catch (e: IOException) {
                                        if (!wrongPin) throw e
                                        false
                                    }
                                }
                            }
                        }
                        val raw = listener.accept()
                        try {
                            WiFiMutualTls(tablet.managers, if (wrongPin) ByteArray(32) { 7 } else host.pin)
                                .authenticateAccepted(raw, 3000).use { socket ->
                                    if (wrongPin) fail("wrong host pin authenticated")
                                    assertEquals(protocol, (socket as SSLSocket).session.protocol)
                                    assertTrue(socket.tcpNoDelay)
                                    assertEquals(0, socket.soTimeout)
                                    socket.soTimeout = 3000
                                    assertEquals(42, socket.inputStream.read())
                                    socket.outputStream.write(43)
                                }
                        } catch (e: IOException) {
                            if (!wrongPin) throw e
                            assertTrue(raw.isClosed)
                        }
                        assertEquals(!wrongPin, hostTask.get(5, TimeUnit.SECONDS))
                    } finally {
                        executor.shutdownNow()
                        assertTrue(executor.awaitTermination(5, TimeUnit.SECONDS))
                    }
                }
            }
        }
    }
}
