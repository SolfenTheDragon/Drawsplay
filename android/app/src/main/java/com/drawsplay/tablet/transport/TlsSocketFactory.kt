package com.drawsplay.tablet.transport

import java.net.InetSocketAddress
import java.net.Socket
import java.security.MessageDigest
import java.security.cert.X509Certificate
import javax.net.ssl.SSLContext
import javax.net.ssl.X509TrustManager

object TlsSocketFactory {
    fun connectPinned(
        host: String,
        port: Int,
        timeoutMs: Int,
        expectedSha256Hex: String,
        sendBufferSize: Int = 0,
        receiveBufferSize: Int = 0
    ): Socket {
        val trustManager = object : X509TrustManager {
            override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) = Unit

            override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
                val leaf = chain?.firstOrNull() ?: throw IllegalStateException("Empty server certificate chain")
                val got = sha256Hex(leaf.encoded)
                if (!got.equals(expectedSha256Hex, ignoreCase = true)) {
                    throw IllegalStateException("TLS fingerprint mismatch")
                }
            }

            override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
        }
        val context = SSLContext.getInstance("TLS")
        context.init(null, arrayOf(trustManager), null)
        val socket = context.socketFactory.createSocket()
        if (sendBufferSize > 0) socket.sendBufferSize = sendBufferSize
        if (receiveBufferSize > 0) socket.receiveBufferSize = receiveBufferSize
        socket.connect(InetSocketAddress(host, port), timeoutMs)
        return socket
    }

    private fun sha256Hex(data: ByteArray): String {
        val digest = MessageDigest.getInstance("SHA-256").digest(data)
        return digest.joinToString("") { "%02x".format(it) }
    }
}

