package com.drawsplay.tablet.transport

import java.io.IOException
import java.net.Socket
import java.net.SocketTimeoutException
import java.security.AlgorithmParameters
import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.security.spec.ECParameterSpec
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference
import javax.net.ssl.KeyManager
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLSocket
import javax.net.ssl.X509TrustManager

/** Paired Wi-Fi only; does not install a process-global security provider. */
class WiFiMutualTls(keyManagers: Array<KeyManager>, pairedHostSpkiSha256: ByteArray) {
    private val managers = keyManagers.copyOf()
    private val pin = pairedHostSpkiSha256.copyOf()

    init {
        require(managers.isNotEmpty()) { "Wi-Fi requires the tablet's Keystore identity" }
        require(pin.size == 32 && pin.any { it != 0.toByte() }) { "Wi-Fi requires a paired host SPKI pin" }
    }

    fun authenticateAccepted(raw: Socket, timeoutMs: Int): Socket {
        var socket: SSLSocket? = null
        val closeFailure = AtomicReference<IOException?>()
        val deadlines = Executors.newSingleThreadScheduledExecutor { task ->
            Thread(task, "wifi-tls-deadline").apply { isDaemon = true }
        }
        try {
            require(timeoutMs in 1..30000) { "Wi-Fi TLS deadline must be 1..30000 ms" }
            require(raw.isConnected && !raw.isClosed) { "Wi-Fi TLS requires a connected socket" }
            val deadline = deadlines.schedule({
                try {
                    raw.close()
                } catch (e: IOException) {
                    closeFailure.set(e)
                }
            }, timeoutMs.toLong(), TimeUnit.MILLISECONDS)
            try {
                val context = SSLContext.getInstance("TLS")
                context.init(managers, arrayOf(pinnedTrust()), null)
                socket = context.socketFactory.createSocket(raw, raw.inetAddress.hostAddress, raw.port, true) as SSLSocket
                val secured = socket
                secured.useClientMode = true
                secured.enabledProtocols = secured.supportedProtocols.filter {
                    it == "TLSv1.2" || it == "TLSv1.3"
                }.toTypedArray()
                secured.enabledCipherSuites = secured.supportedCipherSuites.filter {
                    it in CIPHERS
                }.toTypedArray()
                secured.tcpNoDelay = true
                secured.soTimeout = timeoutMs
                secured.startHandshake()
                if (!deadline.cancel(false)) throw SocketTimeoutException("Wi-Fi TLS total handshake deadline exceeded")
                secured.soTimeout = 0
                return secured
            } catch (e: IOException) {
                if (!deadline.cancel(false)) {
                    throw SocketTimeoutException("Wi-Fi TLS total handshake deadline exceeded").apply {
                        initCause(e)
                        closeFailure.get()?.let { addSuppressed(it) }
                    }
                }
                throw e
            }
        } catch (e: Exception) {
            try {
                raw.close()
            } catch (closeError: IOException) {
                e.addSuppressed(closeError)
            }
            try {
                socket?.close()
            } catch (closeError: IOException) {
                e.addSuppressed(closeError)
            }
            throw e
        } finally {
            deadlines.shutdownNow()
        }
    }

    private fun pinnedTrust(): X509TrustManager = object : X509TrustManager {
        override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()

        override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) {
            throw CertificateException("Tablet context cannot authenticate a TLS client")
        }

        override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
            if (chain == null || chain.isEmpty() || chain.size > 3) throw CertificateException(UNTRUSTED)
            val leaf = chain[0]
            leaf.checkValidity()
            val key = leaf.publicKey as? ECPublicKey ?: throw CertificateException(UNTRUSTED)
            val p256 = AlgorithmParameters.getInstance("EC").run {
                init(ECGenParameterSpec("secp256r1"))
                getParameterSpec(ECParameterSpec::class.java)
            }
            if (key.params.curve != p256.curve || key.params.generator != p256.generator ||
                key.params.order != p256.order || key.params.cofactor != p256.cofactor ||
                leaf.hasUnsupportedCriticalExtension()
            ) throw CertificateException(UNTRUSTED)
            leaf.keyUsage?.let { if (it.isEmpty() || !it[0]) throw CertificateException(UNTRUSTED) }
            leaf.extendedKeyUsage?.let {
                if ("1.3.6.1.5.5.7.3.1" !in it && "2.5.29.37.0" !in it) throw CertificateException(UNTRUSTED)
            }
            if (!MessageDigest.isEqual(pin, MessageDigest.getInstance("SHA-256").digest(key.encoded))) {
                throw CertificateException(UNTRUSTED)
            }
        }
    }

    companion object {
        private const val UNTRUSTED = "Wi-Fi host identity is unpaired, changed or incompatible; re-pair locally"
        private val CIPHERS = setOf(
            "TLS_AES_128_GCM_SHA256", "TLS_AES_256_GCM_SHA384", "TLS_CHACHA20_POLY1305_SHA256",
            "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256", "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384"
        )
    }
}
