package com.drawsplay.tablet.transport

import java.io.Closeable
import java.io.IOException
import java.net.Socket
import java.net.SocketTimeoutException
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import javax.net.ssl.X509KeyManager

/** Accepted Wi-Fi sockets only, off the main thread. Does not start a decoder or input capture. */
class WiFiPairedSession(private val identity: X509KeyManager, pairedHostPin: ByteArray) {
    private val hostPin = pairedHostPin.copyOf()
    private val tls = WiFiMutualTls(arrayOf(identity), hostPin)

    class Control internal constructor(
        val socket: Socket,
        val limits: WiFiSessionProtocol.Hello,
        private val raw: Socket,
        private val offer: WiFiSessionProtocol.Offer
    ) : Closeable {
        private var videoRaw: Socket? = null
        private var video: Socket? = null
        private var attempted = false
        private var closed = false

        @Synchronized
        internal fun takeOffer(): WiFiSessionProtocol.Offer {
            if (closed || attempted) throw IOException("Wi-Fi control ended or video ticket already consumed; reconnect control first")
            attempted = true
            return WiFiSessionProtocol.Offer(offer.id.copyOf(), offer.epoch, offer.ticket.copyOf()).also {
                offer.ticket.fill(0)
            }
        }

        @Synchronized
        internal fun ownVideo(rawSocket: Socket, secured: Socket) {
            if (closed || socket.isClosed) throw IOException("Wi-Fi control ended during video attachment")
            videoRaw = rawSocket
            video = secured
        }

        @Synchronized
        override fun close() {
            if (closed) return
            closed = true
            offer.ticket.fill(0)
            var failure: IOException? = null
            for (resource in listOfNotNull(raw, videoRaw, socket, video)) {
                try {
                    resource.close()
                } catch (error: IOException) {
                    if (failure == null) failure = error else failure.addSuppressed(error)
                }
            }
            failure?.let { throw it }
        }
    }

    fun acceptControl(raw: Socket, capabilities: WiFiSessionProtocol.Hello, timeoutMs: Int): Control =
        exchange(raw, timeoutMs) { secured ->
            val alias = identity.chooseClientAlias(arrayOf("EC"), null, secured)
                ?: throw IOException("Missing tablet Wi-Fi EC identity")
            val leaf = identity.getCertificateChain(alias)?.firstOrNull()
                ?: throw IOException("Missing tablet Wi-Fi identity certificate")
            val nonce = ByteArray(32).also { SecureRandom().nextBytes(it) }
            val hello = capabilities.copy(
                role = WiFiSessionProtocol.TABLET,
                spki = MessageDigest.getInstance("SHA-256").digest(leaf.publicKey.encoded),
                nonce = nonce
            )
            val payload = ByteArray(WiFiSessionProtocol.HELLO_SIZE)
            WiFiSessionProtocol.encodeHello(hello, payload)
            WiFiFrameCodec.Writer(secured.outputStream).writeFrame(
                WiFiFrameCodec.Header().apply { type = WiFiFrameCodec.HELLO; payloadLength = payload.size },
                payload, 0, payload.size
            )
            val reader = WiFiFrameCodec.Reader(secured.inputStream)
            val response = reader.readHeader()
            if (response.type != WiFiFrameCodec.HELLO || response.payloadLength != payload.size || response.captureMonoUs != 0L) {
                throw IOException("Host did not provide compatible Wi-Fi capabilities")
            }
            reader.readPayload(payload)
            val host = WiFiSessionProtocol.decodeHello(payload, WiFiSessionProtocol.HOST, hostPin)
            val limits = WiFiSessionProtocol.negotiate(host, hello)
            val header = reader.readHeader()
            if (header.type != WiFiFrameCodec.CONTROL || header.sequence != 1L ||
                header.payloadLength != 56 || header.captureMonoUs != 0L
            ) throw IOException("Missing initial Wi-Fi session offer")
            val rawOffer = ByteArray(56)
            try {
                reader.readPayload(rawOffer)
                val offer = WiFiSessionProtocol.decodeOffer(rawOffer, header.epoch)
                Control(secured, limits, raw, offer)
            } finally {
                rawOffer.fill(0)
            }
        }

    fun acceptVideo(raw: Socket, control: Control, timeoutMs: Int): Socket {
        try {
            return exchange(raw, timeoutMs) { secured ->
                val offer = control.takeOffer()
                val payload = ByteArray(52)
                try {
                    WiFiSessionProtocol.encodeAttach(offer, payload)
                    WiFiFrameCodec.Writer(secured.outputStream).writeFrame(
                        WiFiFrameCodec.Header().apply {
                            type = WiFiFrameCodec.ATTACH
                            epoch = offer.epoch
                            sequence = 1
                            payloadLength = payload.size
                        }, payload, 0, payload.size
                    )
                    val reader = WiFiFrameCodec.Reader(secured.inputStream)
                    val ready = reader.readHeader()
                    if (ready.type != WiFiFrameCodec.CONTROL || ready.epoch != offer.epoch ||
                        ready.sequence != 1L || ready.payloadLength != 4 || ready.captureMonoUs != 0L
                    ) throw IOException("Host did not associate the Wi-Fi video channel")
                    reader.readPayload(payload)
                    if (payload[0] != 3.toByte() || payload[1] != 0.toByte() ||
                        payload[2] != 0.toByte() || payload[3] != 1.toByte()
                    ) throw IOException("Incompatible Wi-Fi video-ready message")
                    control.ownVideo(raw, secured)
                    secured
                } finally {
                    offer.ticket.fill(0)
                    payload.fill(0)
                }
            }
        } catch (error: Exception) {
            try { control.close() } catch (closeError: IOException) { error.addSuppressed(closeError) }
            throw error
        }
    }

    private fun <T> exchange(raw: Socket, timeoutMs: Int, action: (Socket) -> T): T {
        val deadlines = Executors.newSingleThreadScheduledExecutor { runnable ->
            Thread(runnable, "wifi-session-deadline").apply { isDaemon = true }
        }
        var secured: Socket? = null
        try {
            if (timeoutMs !in 1..5000) throw IOException("Wi-Fi session setup requires a deadline of 1..5000 ms")
            val deadline = deadlines.schedule({ raw.close() }, timeoutMs.toLong(), TimeUnit.MILLISECONDS)
            try {
                secured = tls.authenticateAccepted(raw, timeoutMs)
                secured.soTimeout = timeoutMs
                val result = action(secured)
                if (!deadline.cancel(false)) throw SocketTimeoutException("Wi-Fi session total setup deadline exceeded")
                secured.soTimeout = 0
                return result
            } catch (error: Exception) {
                if (!deadline.cancel(false)) {
                    throw SocketTimeoutException("Wi-Fi session total setup deadline exceeded").apply { initCause(error) }
                }
                throw error
            }
        } catch (error: Exception) {
            try { raw.close() } catch (closeError: IOException) { error.addSuppressed(closeError) }
            try { secured?.close() } catch (closeError: IOException) { error.addSuppressed(closeError) }
            if (error is IOException) throw error
            throw IOException("Cannot establish authenticated Wi-Fi session", error)
        } finally {
            deadlines.shutdownNow()
        }
    }
}
