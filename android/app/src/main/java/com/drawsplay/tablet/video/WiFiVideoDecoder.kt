package com.drawsplay.tablet.video

import android.media.MediaCodec
import android.media.MediaFormat
import android.os.Build
import android.util.Log
import android.view.Surface
import java.io.Closeable
import java.io.IOException
import java.nio.ByteBuffer

/** Synchronous Wi-Fi-only codec owner. Construct, step and close on one decoder thread. */
class WiFiVideoDecoder(
    surface: Surface,
    private val config: WiFiH264.Config,
    private val queue: WiFiAccessUnitQueue,
    maxAccessUnit: Int
) : Closeable {
    private val codec: MediaCodec
    private val info = MediaCodec.BufferInfo()
    private var inputBuffers: Array<ByteBuffer>
    private var reserved = -1
    private var lastPts = 0L
    private var pendingSlot: WiFiAccessUnitQueue.Slot? = null
    private var recoveryDeadlineUs = 0L
    private var picturesSinceFlush = false
    private var csdIndex = 2
    private var closed = false
    var pendingFrames = 0
        private set
    @Volatile var submitted = 0L
        private set
    @Volatile var decoded = 0L
        private set
    /** Release-to-Surface count, not proof of physical scanout or photon latency. */
    @Volatile var renderReleased = 0L
        private set
    @Volatile var decodeDrops = 0L
        private set
    @Volatile var lastOutputUs = System.nanoTime() / 1000
        private set
    val name: String

    init {
        var selected: MediaCodec? = null
        var failure: Exception? = null
        for (lowLatency in if (Build.VERSION.SDK_INT >= 30) booleanArrayOf(true, false) else booleanArrayOf(false)) {
            val candidate = MediaCodec.createDecoderByType(MediaFormat.MIMETYPE_VIDEO_AVC)
            try {
                val format = MediaFormat.createVideoFormat(MediaFormat.MIMETYPE_VIDEO_AVC, config.width, config.height).apply {
                    setInteger(MediaFormat.KEY_MAX_INPUT_SIZE, maxAccessUnit)
                    setInteger(MediaFormat.KEY_FRAME_RATE, config.fps)
                    setByteBuffer("csd-0", ByteBuffer.wrap(config.sps))
                    setByteBuffer("csd-1", ByteBuffer.wrap(config.pps))
                    if (lowLatency && Build.VERSION.SDK_INT >= 30) setInteger(MediaFormat.KEY_LOW_LATENCY, 1)
                }
                candidate.configure(format, surface, null, 0)
                candidate.start()
                selected = candidate
                break
            } catch (error: Exception) {
                candidate.release()
                failure = error
                Log.w(TAG, "Wi-Fi AVC configuration failed (lowLatency=$lowLatency)", error)
            }
        }
        codec = selected ?: throw IOException("Tablet cannot configure Wi-Fi baseline AVC; lower mode or update decoder", failure)
        try {
            name = codec.name
            @Suppress("DEPRECATION")
            inputBuffers = codec.inputBuffers
            for (buffer in inputBuffers) if (buffer.capacity() < maxAccessUnit) {
                throw IOException("Wi-Fi decoder input buffer is smaller than negotiated AU; lower maximum AU and reconnect")
            }
        } catch (error: Exception) {
            codec.release()
            throw error
        }
    }

    /** Nonblocking except for a bounded codec-flush recovery. No app-owned per-frame allocations. */
    fun step(nowUs: Long = System.nanoTime() / 1000): Boolean {
        if (closed) throw IOException("Wi-Fi decoder is closed")
        var worked = false
        var outputCount = 0
        while (outputCount++ < 8) {
            val index = codec.dequeueOutputBuffer(info, 0)
            if (index == MediaCodec.INFO_TRY_AGAIN_LATER) break
            if (index == MediaCodec.INFO_OUTPUT_FORMAT_CHANGED) {
                val format = codec.outputFormat
                val width = format.getInteger(MediaFormat.KEY_WIDTH)
                val height = format.getInteger(MediaFormat.KEY_HEIGHT)
                if (width > ((config.width + 15) / 16) * 16 || height > ((config.height + 15) / 16) * 16) {
                    throw IOException("Wi-Fi decoder changed beyond validated coded dimensions")
                }
                continue
            }
            if (index < 0) continue
            val render = nowUs >= info.presentationTimeUs && nowUs - info.presentationTimeUs <= 2000000L / config.fps
            codec.releaseOutputBuffer(index, render)
            decoded++
            if (pendingFrames > 0) pendingFrames--
            if (render) renderReleased++ else decodeDrops++
            lastOutputUs = nowUs
            worked = true
        }
        if (reserved < 0) reserved = codec.dequeueInputBuffer(0)
        if (reserved < 0) {
            if (pendingSlot != null && nowUs >= recoveryDeadlineUs) {
                throw IOException("Wi-Fi decoder did not resume within 500 ms after IDR flush; reconnect video")
            }
            return worked
        }
        if (csdIndex < 2) {
            val csd = if (csdIndex++ == 0) config.sps else config.pps
            val buffer = inputBuffers[reserved]
            buffer.clear()
            buffer.put(csd)
            codec.queueInputBuffer(reserved, 0, csd.size, 0, MediaCodec.BUFFER_FLAG_CODEC_CONFIG)
            reserved = -1
            return true
        }
        val slot = pendingSlot ?: queue.poll(nowUs) ?: return worked
        pendingSlot = null
        if (slot.restartDecoder && picturesSinceFlush) {
            pendingSlot = slot
            recoveryDeadlineUs = nowUs + 500000
            codec.flush()
            decodeDrops += pendingFrames
            pendingFrames = 0
            picturesSinceFlush = false
            csdIndex = 0
            reserved = -1
            @Suppress("DEPRECATION")
            val refreshed = codec.inputBuffers
            inputBuffers = refreshed
            return true
        }
        try {
            val input = inputBuffers[reserved]
            if (slot.length > input.capacity()) throw IOException("Wi-Fi AU exceeds actual decoder buffer")
            input.clear()
            input.put(slot.bytes, 0, slot.length)
            lastPts = maxOf(slot.receivedUs, lastPts + 1)
            codec.queueInputBuffer(reserved, 0, slot.length, lastPts, 0)
            reserved = -1
            submitted++
            pendingFrames++
            picturesSinceFlush = true
        } finally {
            queue.release(slot)
        }
        return true
    }

    override fun close() {
        if (closed) return
        closed = true
        pendingSlot?.let { queue.release(it) }
        pendingSlot = null
        try {
            codec.stop()
        } finally {
            codec.release()
        }
    }

    /** True while a post-flush recovery IDR is held or CSD is being resent; must keep the drain loop alive. */
    val holdingRecoverySlot: Boolean
        get() = pendingSlot != null || csdIndex < 2

    companion object {
        private const val TAG = "DS-WiFiDecoder"

        fun drained(ended: Boolean, queueDepth: Int, pendingFrames: Int, holdingRecoverySlot: Boolean): Boolean =
            ended && queueDepth == 0 && pendingFrames == 0 && !holdingRecoverySlot
    }
}
