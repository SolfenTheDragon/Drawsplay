package com.drawsplay.tablet.transport

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.fail
import org.junit.Test
import java.io.File
import java.io.IOException
import java.net.InetAddress
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
        withLocalServer { port, pin ->
            TlsSocketFactory.connectPinned("127.0.0.1", port, 3000, pin.uppercase()).use { socket ->
                socket.soTimeout = 3000
                (socket as SSLSocket).startHandshake()
                assertEquals(42, socket.getInputStream().read())
            }
            false
        }
    }

    @Test
    fun wrongCertificatePinRejectsHandshake() {
        withLocalServer { port, _ ->
            TlsSocketFactory.connectPinned("127.0.0.1", port, 3000, "00".repeat(32)).use { socket ->
                socket.soTimeout = 3000
                try {
                    (socket as SSLSocket).startHandshake()
                    fail("wrong certificate pin accepted")
                } catch (expected: SSLException) {
                    // The trust-manager rejection must surface as a failed TLS handshake.
                }
            }
            true
        }
    }

    private fun withLocalServer(test: (Int, String) -> Boolean) {
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
            (context.serverSocketFactory.createServerSocket(
                0, 1, InetAddress.getByName("127.0.0.1")
            ) as SSLServerSocket).use { listener ->
                listener.soTimeout = 5000
                val result = executor.submit<IOException?> {
                    try {
                        (listener.accept() as SSLSocket).use { socket ->
                            socket.soTimeout = 3000
                            socket.startHandshake()
                            socket.getOutputStream().write(42)
                        }
                        null
                    } catch (error: IOException) {
                        error
                    }
                }
                val rejected = test(listener.localPort, pin)
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
