package com.drawsplay.tablet.video

import android.media.MediaCodec
import android.media.MediaCodecList
import android.media.MediaFormat
import android.util.Log
import android.view.Display
import android.view.Surface
import com.drawsplay.tablet.AndroidCompat
import java.nio.ByteBuffer
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicLong

/**
 * Hardware H.264 decoder rendering directly to a [Surface].
 *
 * Configuration is deferred until the stream's SPS and PPS NAL units have
 * both been observed (they carry the real width/height/profile); until then
 * incoming picture NALs are dropped, which is safe because the host opens
 * every session with parameter sets followed by an IDR frame.
 *
 * Low-latency choices:
 *  - Decode output goes straight to the surface (no buffer copies).
 *  - [MediaFormat.KEY_LOW_LATENCY] is requested on Android 11+ so capable
 *    decoders emit frames without internal reordering delay.
 *
 * Frame pacing: decoded frames are released with an explicit presentation
 * timestamp on an even cadence derived from the stream fps ([streamFps]),
 * so SurfaceFlinger latches one frame per source interval regardless of
 * network/decode arrival jitter â€” that jitter otherwise maps 1:1 onto
 * screen time and makes video content judder. The cadence stays at most
 * one frame interval ahead of the clock (bounded latency); a frame whose
 * slot has already passed resyncs and renders immediately, so a paused
 * desktop or a stalled link never delays fresh content.
 *
 * Stall recovery: after a stall upstream of the decoder, the frames held up
 * behind it arrive and decode in a burst. In latency mode a frame that newer
 * pictures are already queued behind is decoded but not rendered
 * ([StaleFrameTracker]), and a keyframe arriving behind a backlog drops the
 * pictures queued ahead of it, so the picture jumps to the newest frame
 * instead of replaying the stall in fast forward.
 *
 * Threading: all methods must be called from a single thread (the video
 * channel's reader thread). Output draining is asynchronous via MediaCodec's
 * callback thread.
 */
class VideoDecoder(
    private val surface: Surface,
    private val onVideoSize: (width: Int, height: Int) -> Unit = { _, _ -> }
) {
    private val initialRefreshHz: Float = try {
        @Suppress("DEPRECATION")
        val display = if (AndroidCompat.supportsWindowMetrics) {
            surface.javaClass.getMethod("getDisplay").invoke(surface) as? Display
        } else {
            null
        }
        display?.refreshRate ?: 0f
    } catch (_: Exception) {
        0f
    }

    private var codec: MediaCodec? = null
    private var vps: ByteArray? = null
    private var sps: ByteArray? = null
    private var pps: ByteArray? = null
    @Volatile
    var streamCodec: String = "h264"
    @Volatile
    var renderMode: String = "latency"
    @Volatile
    var pacingGuardrailApplied: Boolean = false

    /**
     * Source frame rate from the host's config packet; drives render pacing
     * and the surface frame-rate hint. 0 (unknown) falls back to immediate
     * rendering.
     */
    @Volatile
    var streamFps: Int = 0
        set(value) {
            field = value
            applySurfaceFrameRateHint()
        }

        fun displayRefreshHz(): Float {
            return initialRefreshHz
        }

    // Next scheduled presentation time (CLOCK_MONOTONIC ns), maintained by
    // the codec callback thread only.
    private var nextRenderNs = 0L

    // Set by the codec's onError callback; checked in feedNal so the channel
    // tears the connection down and reconnects with a fresh decoder instead
    // of silently feeding a dead codec (black screen).
    private val failed = AtomicBoolean(false)

    // True when this instance runs the software fallback decoder.
    private var usingSoftwareDecoder = false

    // Set once this instance has proven the hardware decoder healthy and
    // reset the shared failure counter.
    private var hwMarkedHealthy = false

    // Decode statistics (M4): written on the reader and codec callback
    // threads, harvested periodically by the stats reporter.
    private val framesReceived = AtomicLong(0)
    private val framesDecoded = AtomicLong(0)
    private val framesRendered = AtomicLong(0)
    private val decodeLatencyUsSum = AtomicLong(0)
    private val presentLatencyUsSum = AtomicLong(0)
    private val frameDrops = AtomicLong(0)
    private val staleFrames = AtomicLong(0)

    /** One harvested statistics interval (all values are deltas). */
    data class Stats(
        val framesReceived: Long,
        val framesRendered: Long,
        val avgDecodeMs: Double,
        val avgPresentMs: Double,
        val frameDrops: Long,
        val queuedFrames: Int,
        /** Frames never shown because newer ones were already queued. */
        val staleFrames: Long
    )

    private var lastReceived = 0L
    private var lastDecoded = 0L
    private var lastRendered = 0L
    private var lastLatencySum = 0L
    private var lastPresentLatencySum = 0L
    private var lastFrameDrops = 0L
    private var lastStaleFrames = 0L

    /**
     * Returns decode statistics since the previous call. Called from the
     * stats reporter thread only.
     */
    /** Frames released for display so far (monotonic). */
    fun renderedFrames(): Long = framesRendered.get()

    fun snapshotStats(): Stats {
        val recv = framesReceived.get()
        val dec = framesDecoded.get()
        val rend = framesRendered.get()
        val latSum = decodeLatencyUsSum.get()
        val dRecv = recv - lastReceived
        val dDec = dec - lastDecoded
        val dRend = rend - lastRendered
        val dLat = latSum - lastLatencySum
        val presentSum = presentLatencyUsSum.get()
        val drops = frameDrops.get()
        val stale = staleFrames.get()
        val dPresent = presentSum - lastPresentLatencySum
        val dDrops = drops - lastFrameDrops
        val dStale = stale - lastStaleFrames
        lastReceived = recv
        lastDecoded = dec
        lastRendered = rend
        lastLatencySum = latSum
        lastPresentLatencySum = presentSum
        lastFrameDrops = drops
        lastStaleFrames = stale
        val avgMs = if (dDec > 0) dLat / 1000.0 / dDec else 0.0
        val avgPresentMs = if (dRend > 0) dPresent / 1000.0 / dRend else 0.0
        val queued = synchronized(pendingLock) { pending.size }
        return Stats(dRecv, dRend, avgMs, avgPresentMs, dDrops, queued, dStale)
    }

    /** True once the decoder is configured and accepting picture NALs. */
    val isConfigured: Boolean
        get() = codec != null

    /**
     * Feeds one Annex-B NAL unit (start code included). Parameter sets are
     * captured until the decoder can be configured; once running they are
     * also fed in-band, so adaptive-bitrate pipeline restarts on the host
     * (which resend SPS/PPS before their IDR) keep decoding seamlessly.
     */
    fun feedNal(nal: ByteArray) {
        if (failed.get()) {
            throw IllegalStateException("decoder failed; needs recreation")
        }
        val type = nalType(nal)
        val spsType = if (streamCodec == "hevc") HEVC_NAL_SPS else AVC_NAL_SPS
        val ppsType = if (streamCodec == "hevc") HEVC_NAL_PPS else AVC_NAL_PPS
        val vpsType = if (streamCodec == "hevc") HEVC_NAL_VPS else -1
        when (type) {
            spsType, ppsType, vpsType -> {
                if (type == spsType) sps = nal
                if (type == ppsType) pps = nal
                if (type == vpsType) vps = nal
                if (codec == null) maybeConfigure() else queueForDecode(nal, countFrame = false)
            }
            else -> {
                val picture =
                    if (streamCodec == "hevc") type in 0..31
                    else type in 1..5
                queueForDecode(nal, countFrame = picture)
            }
        }
    }

    /** Releases the codec. The decoder is unusable afterwards. */
    fun release() {
        try {
            codec?.stop()
        } catch (_: Exception) {
        }
        try {
            codec?.release()
        } catch (_: Exception) {
        }
        codec = null
    }

    private fun maybeConfigure() {
        if (codec != null) return
        val spsData = sps ?: return
        val ppsData = pps ?: return
        val codecName = streamCodec

        // Placeholder size: decoders take the real dimensions from the SPS in
        // csd-0; 0x0 fails configuration on some devices.
        fun buildFormat(vendorKeys: Boolean): MediaFormat? = MediaFormat.createVideoFormat(
            if (codecName == "hevc") MediaFormat.MIMETYPE_VIDEO_HEVC else MediaFormat.MIMETYPE_VIDEO_AVC,
            1920,
            1080
        ).apply {
            if (codecName == "hevc") {
                val vpsData = vps ?: return null
                val joined = ByteArray(vpsData.size + spsData.size + ppsData.size)
                System.arraycopy(vpsData, 0, joined, 0, vpsData.size)
                System.arraycopy(spsData, 0, joined, vpsData.size, spsData.size)
                System.arraycopy(ppsData, 0, joined, vpsData.size + spsData.size, ppsData.size)
                setByteBuffer("csd-0", ByteBuffer.wrap(joined))
            } else {
                setByteBuffer("csd-0", ByteBuffer.wrap(spsData))
                setByteBuffer("csd-1", ByteBuffer.wrap(ppsData))
            }
            // High-bitrate IDR frames (the stream runs up to ~60 Mbps over
            // USB) can exceed a decoder's default input buffer size; an
            // undersized buffer silently drops the keyframe and corrupts
            // the picture until the next IDR.
            setInteger(MediaFormat.KEY_MAX_INPUT_SIZE, MAX_INPUT_BUFFER_SIZE)
            setInteger(MediaFormat.KEY_PRIORITY, 0)
            setInteger(MediaFormat.KEY_OPERATING_RATE, Short.MAX_VALUE.toInt())
            if (AndroidCompat.supportsLowLatencyDecoderHint) {
                setInteger(MediaFormat.KEY_LOW_LATENCY, 1)
            }
            if (vendorKeys) {
                // SoC-specific switches that disable output reordering/queueing
                // on decoders that ignore the standard key. Unknown keys are
                // ignored by most codecs; if one is rejected, configure is
                // retried without them below.
                for ((key, value) in VENDOR_LOW_LATENCY_KEYS) setInteger(key, value)
            }
        }
        val format = buildFormat(vendorKeys = true) ?: return

        val useSoftware = consecutiveHwFailures >= MAX_HW_FAILURES
        val created = try {
            createDecoder(useSoftware, codecName)
        } catch (e: Exception) {
            Log.e(TAG, "Decoder create failed (software=$useSoftware)", e)
            if (!useSoftware) consecutiveHwFailures++
            return
        }
        usingSoftwareDecoder = useSoftware
        try {
            created.setCallback(RenderPacedCallback())
            try {
                created.configure(format, surface, null, 0)
            } catch (e: Exception) {
                Log.w(TAG, "Decoder rejected vendor low-latency keys; retrying without them", e)
                // A failed configure leaves the codec uninitialized, so a
                // fresh instance is needed for the retry.
                val plain = buildFormat(vendorKeys = false) ?: throw e
                try {
                    created.release()
                } catch (_: Exception) {
                }
                val retry = createDecoder(useSoftware, codecName)
                retry.setCallback(RenderPacedCallback())
                retry.configure(plain, surface, null, 0)
                retry.start()
                codec = retry
                usingSoftwareDecoder = useSoftware
                applySurfaceFrameRateHint()
                Log.i(TAG, "Decoder configured: ${retry.name} (software=$useSoftware, no vendor keys)")
                return
            }
            created.start()
        } catch (e: Exception) {
            Log.e(TAG, "Decoder configure/start failed (software=$useSoftware)", e)
            if (!useSoftware) consecutiveHwFailures++
            try {
                created.release()
            } catch (_: Exception) {
            }
            return
        }
        codec = created
        applySurfaceFrameRateHint()
        Log.i(TAG, "Decoder configured: ${created.name} (software=$useSoftware)")
    }

    /**
     * Creates the AVC decoder. The platform default is the hardware decoder
     * on real devices (findDecoderForFormat is unreliable with synthetic
     * formats so it is deliberately not used). After [MAX_HW_FAILURES]
     * hardware codec errors, the software decoder is used instead â€” slower
     * but universally reliable â€” so a device with a broken vendor decoder
     * still gets a picture rather than an endless reconnect loop.
     */
    private fun createDecoder(software: Boolean, codecName: String): MediaCodec {
        val mime = if (codecName == "hevc") MediaFormat.MIMETYPE_VIDEO_HEVC else MediaFormat.MIMETYPE_VIDEO_AVC
        if (software) {
            val name = findSoftwareDecoder(mime)
            if (name != null) {
                Log.w(TAG, "Falling back to software decoder $name after $consecutiveHwFailures hardware failures")
                return MediaCodec.createByCodecName(name)
            }
            Log.w(TAG, "No software decoder found for $mime; retrying platform default")
        }
        return MediaCodec.createDecoderByType(mime)
    }

    private fun findSoftwareDecoder(mime: String): String? {
        return try {
            MediaCodecList(MediaCodecList.REGULAR_CODECS).codecInfos.firstOrNull { info ->
                !info.isEncoder &&
                    info.supportedTypes.any { it.equals(mime, ignoreCase = true) } &&
                    (info.name.startsWith("c2.android.") || info.name.startsWith("OMX.google."))
            }?.name
        } catch (e: Exception) {
            Log.w(TAG, "Codec enumeration failed", e)
            null
        }
    }

    /**
     * Tells the compositor the surface carries fixed-rate video so devices
     * with a refresh rate that is not a multiple of the stream fps (90 Hz
     * panels, adaptive-refresh tablets) can switch to a matching mode
     * instead of latching frames on an uneven 3:2-style cadence.
     */
    private fun applySurfaceFrameRateHint() {
        val fps = streamFps
        if (fps <= 0 || !AndroidCompat.supportsSurfaceFrameRateHint) return
        try {
            if (AndroidCompat.supportsSurfaceFrameRateAlways) {
                surface.setFrameRate(
                    fps.toFloat(),
                    Surface.FRAME_RATE_COMPATIBILITY_FIXED_SOURCE,
                    Surface.CHANGE_FRAME_RATE_ALWAYS
                )
            } else {
                surface.setFrameRate(
                    fps.toFloat(),
                    Surface.FRAME_RATE_COMPATIBILITY_FIXED_SOURCE
                )
            }
        } catch (e: Exception) {
            Log.w(TAG, "setFrameRate failed", e)
        }
    }

    private fun queueForDecode(nal: ByteArray, countFrame: Boolean) {
        val c = codec ?: return
        if (countFrame) framesReceived.incrementAndGet()
        synchronized(pendingLock) {
            enqueuePendingLocked(nal)
            drainPendingLocked(c)
        }
    }

    // Input feeding: MediaCodec async mode hands us input buffer indices on
    // its callback thread; NALs arrive on the reader thread. The pending
    // queue marries the two without blocking either.
    private val pendingLock = Any()
    private val pending = ArrayDeque<ByteArray>()
    private val freeInputs = ArrayDeque<Int>()
    private var awaitingRandomAccess = false

    // Picture starts waiting in [pending]: with the pictures already queued
    // to the codec they tell a decoded frame whether newer ones are coming.
    private var pendingPictureStarts = 0
    private val staleFrameTracker = StaleFrameTracker()

    // Input timestamps are kept strictly increasing so the tracker can match
    // each decoded frame to the picture it came from.
    private var lastInputPtsUs = 0L

    private fun enqueuePendingLocked(nal: ByteArray) {
        val info = DecoderBacklogPolicy.inspect(nal, streamCodec)
        if (awaitingRandomAccess) {
            if (!info.isRandomAccessStart) {
                if (info.startsPicture) staleFrames.incrementAndGet()
                return
            }
            enqueueRecoveryParameterSetsLocked()
            awaitingRandomAccess = false
        }

        pending.addLast(nal)
        if (info.startsPicture) pendingPictureStarts++
        if (info.isCleanRandomAccessStart &&
            pendingPictureStarts - 1 >= KEYFRAME_SKIP_MIN_STALE_PICTURES
        ) {
            skipToKeyframeLocked()
        }
        if (pending.size <= MAX_PENDING_NALS) return

        val result = DecoderBacklogPolicy.trim(
            pending = pending.toList(),
            codec = streamCodec,
            maxPendingNals = MAX_PENDING_NALS
        )
        pending.clear()
        result.retained.forEach(pending::addLast)
        val retainedPictures = DecoderBacklogPolicy.countPictureStarts(pending, streamCodec)
        staleFrames.addAndGet((pendingPictureStarts - retainedPictures).coerceAtLeast(0).toLong())
        pendingPictureStarts = retainedPictures
        awaitingRandomAccess = result.awaitRandomAccess
        if (awaitingRandomAccess) {
            Log.w(TAG, "Decoder backlog dropped; waiting for next random-access frame")
        }
    }

    /**
     * A keyframe just arrived behind pictures still waiting for decode: those
     * are older than the keyframe and nothing after it needs them, so they
     * are dropped instead of being decoded only to be thrown away.
     */
    private fun skipToKeyframeLocked() {
        val result = DecoderBacklogPolicy.skipToCleanRandomAccess(pending.toList(), streamCodec)
            ?: return
        pending.clear()
        result.retained.forEach(pending::addLast)
        pendingPictureStarts = DecoderBacklogPolicy.countPictureStarts(pending, streamCodec)
        staleFrames.addAndGet(result.droppedPictures.toLong())
        Log.i(TAG, "Skipped ${result.droppedPictures} stale pictures to a queued keyframe")
    }

    private fun enqueueRecoveryParameterSetsLocked() {
        if (streamCodec == "hevc") {
            vps?.let(pending::addLast)
        }
        sps?.let(pending::addLast)
        pps?.let(pending::addLast)
    }

    private fun drainPendingLocked(c: MediaCodec) {
        while (pending.isNotEmpty() && freeInputs.isNotEmpty()) {
            val index = freeInputs.removeFirst()
            val nal = pending.removeFirst()
            val startsPicture = DecoderBacklogPolicy.startsPicture(nal, streamCodec)
            if (startsPicture && pendingPictureStarts > 0) pendingPictureStarts--
            try {
                val buffer = c.getInputBuffer(index) ?: continue
                if (buffer.capacity() < nal.size) {
                    // Shouldn't happen with KEY_MAX_INPUT_SIZE set, but a
                    // decoder that ignores the hint must not crash the feed.
                    Log.w(TAG, "NAL (${nal.size} B) exceeds input buffer (${buffer.capacity()} B); dropped")
                    freeInputs.addFirst(index)
                    continue
                }
                buffer.clear()
                buffer.put(nal)
                val ptsUs = maxOf(System.nanoTime() / 1000, lastInputPtsUs + 1)
                lastInputPtsUs = ptsUs
                c.queueInputBuffer(index, 0, nal.size, ptsUs, 0)
                if (startsPicture) staleFrameTracker.onPictureQueued(ptsUs)
            } catch (e: Exception) {
                Log.w(TAG, "queueInputBuffer failed", e)
                return
            }
        }
    }

    private inner class RenderPacedCallback : MediaCodec.Callback() {
        override fun onInputBufferAvailable(codec: MediaCodec, index: Int) {
            synchronized(pendingLock) {
                freeInputs.addLast(index)
                drainPendingLocked(codec)
            }
        }

        override fun onOutputBufferAvailable(
            codec: MediaCodec,
            index: Int,
            info: MediaCodec.BufferInfo
        ) {
            // presentationTimeUs carries the queue-time clock (see
            // drainPendingLocked), so now - pts = in-codec decode latency.
            val nowNs = System.nanoTime()
            val latencyUs = nowNs / 1000 - info.presentationTimeUs
            if (latencyUs in 0..10_000_000) {
                decodeLatencyUsSum.addAndGet(latencyUs)
            }
            val decoded = framesDecoded.incrementAndGet()
            // A hardware decoder that sustains a couple of seconds of output
            // is healthy: clear the shared failure count so a one-off glitch
            // long ago doesn't consign future sessions to software decoding.
            if (!usingSoftwareDecoder && !hwMarkedHealthy && decoded >= HW_HEALTHY_FRAMES) {
                hwMarkedHealthy = true
                consecutiveHwFailures = 0
            }
            val fps = streamFps
            // Asked for every output so the tracker stays matched to the
            // codec; only latency mode acts on the answer.
            val current = synchronized(pendingLock) {
                staleFrameTracker.shouldRender(
                    outputPtsUs = info.presentationTimeUs,
                    pendingPictures = pendingPictureStarts,
                    frameIntervalNs = if (fps > 0) 1_000_000_000L / fps else 0L,
                    nowNs = nowNs
                )
            }
            try {
                if (fps > 0 && renderMode == "smooth") {
                    // Paced release: keep an even presentation cadence at the
                    // stream interval, at most one interval ahead of now.
                    // Late or first frames resync to "now" (rendered at the
                    // next vsync), so pacing never adds unbounded latency.
                    framesRendered.incrementAndGet()
                    val intervalNs = 1_000_000_000L / fps
                    val now = System.nanoTime()
                    var renderAt = nextRenderNs
                    if (renderAt < now || renderAt > now + 2 * intervalNs) {
                        if (renderAt > 0 && renderAt + intervalNs < now) {
                            frameDrops.incrementAndGet()
                        }
                        renderAt = now
                    }
                    nextRenderNs = renderAt + intervalNs
                    val presentDelayUs = (renderAt - now).coerceAtLeast(0L) / 1000L
                    presentLatencyUsSum.addAndGet(presentDelayUs)
                    codec.releaseOutputBuffer(index, renderAt)
                } else if (current) {
                    // Latency mode (default): present immediately.
                    framesRendered.incrementAndGet()
                    presentLatencyUsSum.addAndGet(0L)
                    codec.releaseOutputBuffer(index, System.nanoTime())
                } else {
                    // Newer frames are already queued behind this one after a
                    // stall: showing it would replay the stall in fast forward.
                    staleFrames.incrementAndGet()
                    codec.releaseOutputBuffer(index, false)
                }
            } catch (e: Exception) {
                Log.w(TAG, "releaseOutputBuffer failed", e)
            }
        }

        override fun onError(codec: MediaCodec, e: MediaCodec.CodecException) {
            Log.e(TAG, "Decoder error (recoverable=${e.isRecoverable} transient=${e.isTransient})", e)
            if (!usingSoftwareDecoder && !hwMarkedHealthy) consecutiveHwFailures++
            failed.set(true)
        }

        override fun onOutputFormatChanged(codec: MediaCodec, format: MediaFormat) {
            Log.i(TAG, "Decoder output format: $format")
            val width = if (format.containsKey("crop-left") && format.containsKey("crop-right")) {
                format.getInteger("crop-right") - format.getInteger("crop-left") + 1
            } else {
                format.getInteger(MediaFormat.KEY_WIDTH)
            }
            val height = if (format.containsKey("crop-top") && format.containsKey("crop-bottom")) {
                format.getInteger("crop-bottom") - format.getInteger("crop-top") + 1
            } else {
                format.getInteger(MediaFormat.KEY_HEIGHT)
            }
            if (width > 0 && height > 0) onVideoSize(width, height)
        }
    }

    private fun nalType(nal: ByteArray): Int {
        // Skip the 3- or 4-byte start code, then mask the nal_unit_type.
        val offset = if (nal.size > 3 && nal[2].toInt() == 1) 3 else 4
        if (nal.size <= offset) return -1
        return if (streamCodec == "hevc") {
            (nal[offset].toInt() ushr 1) and 0x3F
        } else {
            nal[offset].toInt() and 0x1F
        }
    }

    companion object {
        private const val TAG = "VideoDecoder"
        private const val AVC_NAL_SPS = 7
        private const val AVC_NAL_PPS = 8
        private const val HEVC_NAL_VPS = 32
        private const val HEVC_NAL_SPS = 33
        private const val HEVC_NAL_PPS = 34
        private const val MAX_PENDING_NALS = 128

        private val VENDOR_LOW_LATENCY_KEYS = listOf(
            "vendor.qti-ext-dec-low-latency.enable" to 1,
            "vendor.rtc-ext-dec-low-latency.enable" to 1,
            "vendor.low-latency.enable" to 1,
            "vendor.hisi-ext-low-latency-video-dec.video-scene-for-low-latency-req" to 1,
            "vendor.hisi-ext-low-latency-video-dec.video-scene-for-low-latency-rdy" to -1
        )

        /**
         * Pictures waiting ahead of a newly arrived keyframe before they are
         * dropped for it: deeper than ordinary arrival jitter, so only a
         * backlog from a stall skips straight to the keyframe.
         */
        private const val KEYFRAME_SKIP_MIN_STALE_PICTURES = 3

        /** 4 MB input buffers: covers the largest IDR at the 60 Mbps cap. */
        private const val MAX_INPUT_BUFFER_SIZE = 4 * 1024 * 1024

        /** Hardware codec errors before falling back to software decoding. */
        private const val MAX_HW_FAILURES = 3

        /** Decoded frames after which a hardware decoder is deemed healthy. */
        private const val HW_HEALTHY_FRAMES = 120L

        /**
         * Hardware decoder failures across decoder instances (each failure
         * tears down the channel, which reconnects with a new instance).
         * Only touched from codec callback / reader threads; volatile is
         * enough for this heuristic.
         */
        @Volatile
        private var consecutiveHwFailures = 0
    }
}
