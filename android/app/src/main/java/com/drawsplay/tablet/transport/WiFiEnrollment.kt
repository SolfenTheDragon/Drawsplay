package com.drawsplay.tablet.transport

import java.io.DataInputStream
import java.io.IOException
import java.net.Socket
import java.net.SocketTimeoutException
import java.security.MessageDigest
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import javax.net.ssl.X509KeyManager

/** A verified identity awaiting local approval and durable trust, not an admitted display session. */
data class WiFiPendingEnrollment(val socket: Socket, val hostSpkiSha256: ByteArray)

/** Single enrollment at a time, off the UI thread; no shared USB code or global TLS state. */
class WiFiEnrollment(private val identity: X509KeyManager) {
    fun authenticateAccepted(raw: Socket, code: String, timeoutMs: Int): WiFiPendingEnrollment {
        val deadlines = Executors.newSingleThreadScheduledExecutor { action ->
            Thread(action, "wifi-enrollment-deadline").apply { isDaemon = true }
        }
        var secured: Socket? = null
        try {
            if (timeoutMs !in 1..120000 || code.length != 8 || code.any { it !in '0'..'9' }) {
                throw IOException("Wi-Fi enrollment requires an eight-digit code and a deadline of 1..120000 ms")
            }
            if (!raw.isConnected || raw.isClosed) throw IOException("Wi-Fi enrollment requires a connected socket")
            val deadline = deadlines.schedule({ raw.close() }, timeoutMs.toLong(), TimeUnit.MILLISECONDS)
            try {
                raw.tcpNoDelay = true
                raw.soTimeout = timeoutMs
                val input = DataInputStream(raw.getInputStream())
                val output = raw.getOutputStream()
                val preface = ByteArray(8)
                input.readFully(preface)
                if (!preface.contentEquals(byteArrayOf(68, 83, 69, 80, 0, 2, 0, 0))) {
                    throw IOException("Incompatible Wi-Fi pairing version; update both host and tablet")
                }
                val hostPin = ByteArray(32).also { input.readFully(it) }
                if (hostPin.all { it == 0.toByte() }) throw IOException("Wi-Fi enrollment host identity is missing")
                val alias = identity.chooseClientAlias(arrayOf("EC"), null, raw)
                    ?: throw IOException("Wi-Fi enrollment requires a tablet EC identity")
                val certificate = identity.getCertificateChain(alias)?.firstOrNull()
                    ?: throw IOException("Wi-Fi enrollment tablet certificate is missing")
                certificate.checkValidity()
                val tabletPin = MessageDigest.getInstance("SHA-256").digest(certificate.publicKey.encoded)
                output.write(tabletPin)
                output.flush()
                val salt = ByteArray(16).also { input.readFully(it) }
                val nonce = ByteArray(32).also { input.readFully(it) }
                val publicB = ByteArray(384).also { input.readFully(it) }
                WiFiPairingClient(code, hostPin, tabletPin, salt, nonce, publicB).use { client ->
                    output.write(client.publicValue())
                    val proof = client.proof()
                    try {
                        output.write(proof)
                        output.flush()
                    } finally {
                        proof.fill(0)
                    }
                    val confirmation = ByteArray(32)
                    try {
                        input.readFully(confirmation)
                        if (!client.verifyHost(confirmation)) {
                            throw IOException("Wi-Fi pairing failed; verify the code and reopen pairing locally")
                        }
                    } finally {
                        confirmation.fill(0)
                    }
                }
                secured = WiFiMutualTls(arrayOf(identity), hostPin).authenticateAccepted(raw, minOf(timeoutMs, 30000))
                if (!deadline.cancel(false)) throw SocketTimeoutException("Wi-Fi enrollment total deadline exceeded")
                return WiFiPendingEnrollment(secured, hostPin)
            } catch (error: Exception) {
                if (!deadline.cancel(false)) {
                    throw SocketTimeoutException("Wi-Fi enrollment total deadline exceeded").apply { initCause(error) }
                }
                throw error
            }
        } catch (error: Exception) {
            try {
                raw.close()
            } catch (closeError: IOException) {
                error.addSuppressed(closeError)
            }
            try {
                secured?.close()
            } catch (closeError: IOException) {
                error.addSuppressed(closeError)
            }
            if (error is IOException) throw error
            throw IOException("Cannot authenticate Wi-Fi enrollment; no trust was saved", error)
        } finally {
            deadlines.shutdownNow()
        }
    }
}
