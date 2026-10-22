package com.drawsplay.tablet.transport

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import java.io.File
import java.io.IOException
import java.net.InetAddress
import java.net.ServerSocket
import java.net.SocketTimeoutException
import java.nio.file.Files
import java.security.KeyStore
import java.security.MessageDigest
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import javax.net.ssl.KeyManagerFactory
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLException
import javax.net.ssl.SSLServerSocket
import javax.net.ssl.SSLSocket

class TlsSocketFactoryTest {
    @Test
    fun pinnedCertificateAllowsEncryptedExchange() {
        withLocalServer { port, pin, _ ->
            TlsSocketFactory.connectPinned("127.0.0.1", port, 3000, pin.uppercase()).use { socket ->
                assertTrue((socket as SSLSocket).session.isValid)
                assertTrue(socket.tcpNoDelay)
                assertEquals(0, socket.soTimeout)
                socket.soTimeout = 3000
                assertEquals(42, socket.getInputStream().read())
            }
            false
        }
    }

    @Test
    fun wrongCertificatePinRejectsHandshake() {
        withLocalServer { port, _, _ ->
            try {
                TlsSocketFactory.connectPinned("127.0.0.1", port, 3000, "00".repeat(32)).use {
                    fail("factory returned an unauthenticated socket")
                }
            } catch (expected: SSLException) {
                // Authentication must finish before a socket is returned to a channel.
            }
            true
        }
    }

    @Test
    fun stalledHandshakeTimesOutAndClosesConnection() {
        ServerSocket(0, 1, InetAddress.getByName("127.0.0.1")).use { listener ->
            listener.soTimeout = 3000
            val executor = Executors.newSingleThreadExecutor()
            try {
                val closed = executor.submit<Boolean> {
                    listener.accept().use { peer ->
                        peer.soTimeout = 3000
                        val input = peer.getInputStream()
                        while (input.read() != -1) {
                            // Consume ClientHello but never answer it.
                        }
                        true
                    }
                }
                try {
                    TlsSocketFactory.connectPinned(
                        "127.0.0.1", listener.localPort, 200, "00".repeat(32)
                    ).use { fail("factory returned before completing handshake") }
                } catch (expected: SocketTimeoutException) {
                    // A reachable peer that never speaks TLS cannot stall reconnection.
                }
                assertTrue(closed.get(5, TimeUnit.SECONDS))
            } finally {
                executor.shutdownNow()
                check(executor.awaitTermination(5, TimeUnit.SECONDS))
            }
        }
    }

    @Test
    fun invalidPinOrUnboundedTimeoutIsRejectedBeforeDialing() {
        for ((pin, timeout) in listOf(
            "" to 1000, "z".repeat(64) to 1000, "00".repeat(32) to 0,
            "00".repeat(32) to -1
        )) {
            try {
                TlsSocketFactory.connectPinned("127.0.0.1", 1, timeout, pin).use {
                    fail("invalid TLS connection parameters accepted")
                }
                fail("invalid TLS connection parameters accepted")
            } catch (expected: IllegalArgumentException) {
                // Bad policy is rejected locally, not retried as a remote socket failure.
            }
        }
    }

    @Test
    fun hostInitiatedTcpConnectionRetainsHostTlsServerIdentity() {
        withLocalServer(hostInitiates = true) { _, pin, listener ->
            TlsSocketFactory.authenticateAccepted(listener.accept(), 3000, pin).use { socket ->
                assertTrue((socket as SSLSocket).useClientMode)
                assertTrue(socket.session.isValid)
                socket.soTimeout = 3000
                assertEquals(42, socket.getInputStream().read())
            }
            false
        }
    }

    @Test
    fun hostInitiatedConnectionWithWrongPinClosesAcceptedSocket() {
        withLocalServer(hostInitiates = true) { _, _, listener ->
            val accepted = listener.accept()
            try {
                TlsSocketFactory.authenticateAccepted(accepted, 3000, "00".repeat(32)).use {
                    fail("unauthenticated incoming host accepted")
                }
            } catch (expected: SSLException) {
                assertTrue(accepted.isClosed)
            } finally {
                accepted.close()
            }
            true
        }
    }

    @Test
    fun stalledAcceptedHandshakeTimesOutAndClosesConnection() {
        ServerSocket(0, 1, InetAddress.getByName("127.0.0.1")).use { listener ->
            listener.soTimeout = 3000
            val executor = Executors.newSingleThreadExecutor()
            try {
                val closed = executor.submit<Boolean> {
                    java.net.Socket("127.0.0.1", listener.localPort).use { peer ->
                        peer.soTimeout = 3000
                        val input = peer.getInputStream()
                        while (input.read() != -1) {
                            // Host connected but never sends its TLS server response.
                        }
                        true
                    }
                }
                val accepted = listener.accept()
                try {
                    TlsSocketFactory.authenticateAccepted(
                        accepted, 200, "00".repeat(32)
                    ).use { fail("incoming host returned without a TLS handshake") }
                } catch (expected: SocketTimeoutException) {
                    assertTrue(accepted.isClosed)
                } finally {
                    accepted.close()
                }
                assertTrue(closed.get(5, TimeUnit.SECONDS))
            } finally {
                executor.shutdownNow()
                check(executor.awaitTermination(5, TimeUnit.SECONDS))
            }
        }
    }

    private fun withLocalServer(
        hostInitiates: Boolean = false,
        test: (Int, String, ServerSocket) -> Boolean
    ) {
        val directory = Files.createTempDirectory("drawsplay-wifi-tls-test").toFile()
        val keyFile = File(directory, "identity.p12")
        val logFile = File(directory, "keytool.log")
        val executor = Executors.newSingleThreadExecutor()
        try {
            val password = "temporary-test-identity"
            val keytool = File(System.getProperty("java.home"), "bin/keytool").absolutePath
            val generate = ProcessBuilder(
                keytool, "-genkeypair", "-alias", "wifi-test", "-keyalg", "EC",
                "-groupname", "secp256r1", "-dname", "CN=drawsplay.local",
                "-validity", "1", "-storetype", "PKCS12", "-keystore", keyFile.absolutePath,
                "-storepass", password, "-keypass", password, "-noprompt"
            ).redirectErrorStream(true).redirectOutput(logFile).start()
            if (!generate.waitFor(15, TimeUnit.SECONDS)) {
                generate.destroyForcibly()
                check(generate.waitFor(5, TimeUnit.SECONDS)) { "test keytool failed to stop" }
                error("test certificate generation timed out")
            }
            check(generate.exitValue() == 0) { "test certificate generation failed: ${logFile.readText()}" }
            val store = KeyStore.getInstance("PKCS12")
            keyFile.inputStream().use { store.load(it, password.toCharArray()) }
            val managers = KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm())
            managers.init(store, password.toCharArray())
            val context = SSLContext.getInstance("TLS")
            context.init(managers.keyManagers, null, null)
            val pin = MessageDigest.getInstance("SHA-256").digest(store.getCertificate("wifi-test").encoded)
                .joinToString("") { "%02x".format(it) }
            val server = if (hostInitiates) {
                ServerSocket(0, 1, InetAddress.getByName("127.0.0.1"))
            } else {
                context.serverSocketFactory.createServerSocket(
                0, 1, InetAddress.getByName("127.0.0.1")
                ) as SSLServerSocket
            }
            server.use { listener ->
                listener.soTimeout = 5000
                val result = executor.submit<IOException?> {
                    try {
                        val peer = if (hostInitiates) {
                            (context.socketFactory.createSocket("127.0.0.1", listener.localPort)
                                as SSLSocket).apply { useClientMode = false }
                        } else {
                            listener.accept() as SSLSocket
                        }
                        peer.use { socket ->
                            socket.soTimeout = 3000
                            socket.startHandshake()
                            socket.getOutputStream().write(42)
                        }
                        null
                    } catch (error: IOException) {
                        error
                    }
                }
                val rejected = test(listener.localPort, pin, listener)
                val error = result.get(10, TimeUnit.SECONDS)
                if (rejected) assertNotNull(error) else assertNull(error)
            }
        } finally {
            executor.shutdownNow()
            check(executor.awaitTermination(10, TimeUnit.SECONDS)) { "TLS test worker did not stop" }
            Files.deleteIfExists(keyFile.toPath())
            Files.deleteIfExists(logFile.toPath())
            Files.delete(directory.toPath())
        }
    }
}
