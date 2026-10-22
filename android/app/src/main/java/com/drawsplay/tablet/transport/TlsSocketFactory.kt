package com.drawsplay.tablet.transport

import java.net.InetSocketAddress
import java.net.Socket
import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLSocket
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
        require(timeoutMs > 0) { "TLS connection timeout must be positive" }
        val socket = pinnedContext(expectedSha256Hex).socketFactory.createSocket() as SSLSocket
        return closeOnFailure(socket) {
            if (sendBufferSize > 0) socket.sendBufferSize = sendBufferSize
            if (receiveBufferSize > 0) socket.receiveBufferSize = receiveBufferSize
            socket.connect(InetSocketAddress(host, port), timeoutMs)
            authenticate(socket, timeoutMs)
        }
    }

    fun authenticateAccepted(
        connectedSocket: Socket,
        timeoutMs: Int,
        expectedSha256Hex: String
    ): Socket = closeOnFailure(connectedSocket) {
        require(timeoutMs > 0) { "TLS connection timeout must be positive" }
        require(connectedSocket.isConnected && !connectedSocket.isClosed) {
            "TLS requires a connected socket"
        }
        val socket = pinnedContext(expectedSha256Hex).socketFactory.createSocket(
            connectedSocket,
            connectedSocket.inetAddress.hostAddress,
            connectedSocket.port,
            true
        ) as SSLSocket
        closeOnFailure(socket) {
            // TCP initiation and TLS identity roles are deliberately independent.
            socket.useClientMode = true
            authenticate(socket, timeoutMs)
        }
    }

    private fun pinnedContext(expectedSha256Hex: String): SSLContext {
        require(expectedSha256Hex.length == 64 && expectedSha256Hex.all {
            it in '0'..'9' || it in 'a'..'f' || it in 'A'..'F'
        }) { "TLS certificate pin must contain 64 hexadecimal characters" }
        val trustManager = object : X509TrustManager {
            override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) = Unit

            override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
                val leaf = chain?.firstOrNull() ?: throw CertificateException("Empty server certificate chain")
                val got = sha256Hex(leaf.encoded)
                if (!got.equals(expectedSha256Hex, ignoreCase = true)) {
                    throw CertificateException("TLS fingerprint mismatch")
                }
            }

            override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
        }
        val context = SSLContext.getInstance("TLS")
        context.init(null, arrayOf(trustManager), null)
        return context
    }

    private fun authenticate(socket: SSLSocket, timeoutMs: Int): Socket {
        socket.tcpNoDelay = true
        socket.soTimeout = timeoutMs
        socket.startHandshake()
        socket.soTimeout = 0
        return socket
    }

    private inline fun <T> closeOnFailure(socket: Socket, action: () -> T): T {
        try {
            return action()
        } catch (error: Exception) {
            try {
                socket.close()
            } catch (closeError: Exception) {
                error.addSuppressed(closeError)
            }
            throw error
        }
    }

    private fun sha256Hex(data: ByteArray): String {
        val digest = MessageDigest.getInstance("SHA-256").digest(data)
        return digest.joinToString("") { "%02x".format(it) }
    }
}
