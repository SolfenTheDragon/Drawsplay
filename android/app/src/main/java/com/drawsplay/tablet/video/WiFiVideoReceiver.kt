package com.drawsplay.tablet.video

import com.drawsplay.tablet.transport.WiFiFrameCodec
import com.drawsplay.tablet.transport.WiFiSessionProtocol
import java.io.IOException
import java.io.InputStream

/** Only for an associated paired video socket; no USB framing or decoder sharing. */
class WiFiVideoReceiver(
    input: InputStream,
    private val limits: WiFiSessionProtocol.Hello,
    private val epoch: Long,
    private val nowUs: () -> Long = { System.nanoTime() / 1000 }
) {
    private val reader = WiFiFrameCodec.Reader(input)
    val config: WiFiH264.Config
    val queue: WiFiAccessUnitQueue
    private val h264: WiFiH264
    private var nextSequence = 3L
    private var ended = false
    private val endMessage = ByteArray(4)
    @Volatile var received = 0L
        private set
    var maxInterArrivalUs = 0L
        private set
    var burstArrivals = 0L
        private set
    private var lastArrivalUs = 0L
    private val queueWaitUs: Long

    init {
        val header = reader.readHeader()
        if (epoch == 0L || header.epoch != epoch || header.sequence != 2L ||
            header.type != WiFiFrameCodec.CONFIG || header.captureMonoUs != 0L
        ) throw IOException("Missing initial Wi-Fi decoder config in this paired session")
        val raw = ByteArray(header.payloadLength)
        reader.readPayload(raw)
        config = WiFiH264.decodeConfig(raw, limits)
        h264 = WiFiH264(config)
        val maxAgeUs = maxQueueAgeUs(config.fps)
        queue = WiFiAccessUnitQueue(limits.maxAccessUnit, limits.queueSlots, limits.queueBytes, maxAgeUs)
        queueWaitUs = maxAgeUs
    }

    /** Network-reader thread only. Receives directly into a reusable queue slot. */
    fun receive(): Boolean {
        if (ended) throw IOException("Wi-Fi video already ended; reconnect before receiving")
        val header = reader.readHeader()
        if (header.epoch != epoch || header.sequence != nextSequence) {
            throw IOException("Invalid Wi-Fi video epoch or sequence; reconnect video")
        }
        if (header.type == WiFiFrameCodec.CONTROL && header.payloadLength == 4 && header.captureMonoUs == 0L) {
            reader.readPayload(endMessage)
            if (endMessage[0] != 5.toByte() || endMessage[1] != 0.toByte() ||
                endMessage[2] != 0.toByte() || endMessage[3] != 1.toByte()
            ) throw IOException("Unsupported Wi-Fi video control record")
            ended = true
            return false
        }
        if (header.type != WiFiFrameCodec.VIDEO_AU || header.payloadLength > limits.maxAccessUnit ||
            header.captureMonoUs < 0L || header.durationUs != 1000000L / config.fps
        ) throw IOException("Invalid Wi-Fi video sequence, type, duration or negotiated AU size; reconnect video")
        // Late pictures still decode: the newest-output renderer skips the
        // stale ones, whereas a discard adds an IDR round trip on top.
        val slot = queue.acquire(queueWaitUs)
        try {
            val length = reader.readPayload(slot.bytes)
            val idr = h264.inspect(slot.bytes, length)
            val arrival = nowUs()
            if (received != 0L) {
                val gap = arrival - lastArrivalUs
                maxInterArrivalUs = maxOf(maxInterArrivalUs, gap)
                if (gap < 1000) burstArrivals++
            }
            lastArrivalUs = arrival
            queue.publish(slot, length, idr, arrival)
        } catch (error: Exception) {
            try { queue.abandon(slot) } catch (closeError: IOException) { error.addSuppressed(closeError) }
            throw error
        }
        received++
        nextSequence++
        return true
    }

    companion object {
        /**
         * A queued picture older than this means the decoder stalled, not
         * jittered: discard and resume at the next IDR. Shorter bounds turn
         * ordinary decode jitter into a freeze lasting until the next IDR.
         */
        fun maxQueueAgeUs(fps: Int): Long = maxOf(100_000L, 4_000_000L / fps)
    }
}
