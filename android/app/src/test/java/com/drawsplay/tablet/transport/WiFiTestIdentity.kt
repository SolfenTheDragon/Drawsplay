package com.drawsplay.tablet.transport

import java.io.File
import java.nio.file.Files
import java.security.KeyStore
import java.util.concurrent.TimeUnit
import javax.net.ssl.KeyManagerFactory
import javax.net.ssl.X509KeyManager
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue

internal fun testWiFiIdentity(): X509KeyManager {
    val directory = Files.createTempDirectory("drawsplay-wifi-identity-test").toFile()
    try {
        val file = File(directory, "identity.p12")
        val log = File(directory, "keytool.log")
        val process = ProcessBuilder(
            File(System.getProperty("java.home"), "bin/keytool").absolutePath,
            "-genkeypair", "-alias", "test", "-keyalg", "EC", "-groupname", "secp256r1",
            "-dname", "CN=WiFi test", "-validity", "1", "-storetype", "PKCS12",
            "-keystore", file.absolutePath, "-storepass", "public-test-only", "-noprompt"
        ).redirectErrorStream(true).redirectOutput(log).start()
        try {
            assertTrue(process.waitFor(10, TimeUnit.SECONDS))
            assertEquals(log.readText(), 0, process.exitValue())
        } finally {
            process.destroyForcibly()
        }
        val store = KeyStore.getInstance("PKCS12")
        file.inputStream().use { store.load(it, "public-test-only".toCharArray()) }
        return KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm()).run {
            init(store, "public-test-only".toCharArray())
            keyManagers.filterIsInstance<X509KeyManager>().single()
        }
    } finally {
        check(directory.deleteRecursively()) { "Temporary test identity cleanup failed" }
    }
}
