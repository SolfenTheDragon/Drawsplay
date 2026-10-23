package com.drawsplay.tablet.video

import android.util.Log
import android.view.Surface
import com.drawsplay.tablet.transport.WiFiControlLink
import com.drawsplay.tablet.transport.WiFiSessionProtocol
import java.io.Closeable
import java.io.IOException
import java.io.InputStream
import java.util.concurrent.CompletableFuture
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference

/** The decoder surface the player drives; implemented by [WiFiVideoDecoder] in production. */
interface PlayerDecoder : Closeable {
    val pendingFrames: Int
    val holdingRecoverySlot: Boolean
    val lastOutputUs: Long
    val renderReleased: Long get() = 0L
    val decodeDrops: Long get() = 0L
    fun step(nowUs: Long): Boolean
}

private class MediaCodecPlayerDecoder(private val decoder: WiFiVideoDecoder) : PlayerDecoder {
    override val pendingFrames get() = decoder.pendingFrames
    override val holdingRecoverySlot get() = decoder.holdingRecoverySlot
    override val lastOutputUs get() = decoder.lastOutputUs
    override val renderReleased get() = decoder.renderReleased
    override val decodeDrops get() = decoder.decodeDrops
    override fun step(nowUs: Long) = decoder.step(nowUs)
    override fun close() = decoder.close()
}

/** Plays one paired Wi-Fi video session: receive on the caller thread, decode on a dedicated thread. */
object WiFiVideoPlayer {
    private const val TAG = "WiFiVideoPlayer"
    private const val STALL_US = 2_000_000L
    private val DECODER_READY = byteArrayOf(7, 0, 0, 1)
    private const val STATS_INTERVAL_MS = 500L
    private const val KEYFRAME_POLL_MS = 20L

    fun run(
        media: InputStream,
        closeMedia: () -> Unit,
        link: WiFiControlLink,
        limits: WiFiSessionProtocol.Hello,
        epoch: Long,
        surface: Surface,
        statsLink: WiFiControlLink?,
        linkSample: () -> WiFiVideoStats.Link?,
        cancelled: () -> Boolean
    ) = run(media, closeMedia, link, limits, epoch, { config, queue ->
        MediaCodecPlayerDecoder(WiFiVideoDecoder(surface, config, queue, limits.maxAccessUnit))
    }, statsLink, STATS_INTERVAL_MS, linkSample, cancelled)

    fun run(
        media: InputStream,
        closeMedia: () -> Unit,
        link: WiFiControlLink,
        limits: WiFiSessionProtocol.Hello,
        epoch: Long,
        decoderFactory: (WiFiH264.Config, WiFiAccessUnitQueue) -> PlayerDecoder,
        statsLink: WiFiControlLink? = null,
        statsIntervalMs: Long = STATS_INTERVAL_MS,
        linkSample: () -> WiFiVideoStats.Link? = { null },
        cancelled: () -> Boolean
    ) {
        val receiver = WiFiVideoReceiver(media, limits, epoch)
        val ready = CompletableFuture<Unit>()
        val ended = AtomicBoolean(false)
        val failure = AtomicReference<Throwable>()
        val active = AtomicReference<PlayerDecoder>()
        val thread = Thread({
            try {
                decoderFactory(receiver.config, receiver.queue).use { decoder ->
                    active.set(decoder)
                    ready.complete(Unit)
                    var progressUs = System.nanoTime() / 1000
                    var seenPending = decoder.pendingFrames
                    var seenOutputUs = decoder.lastOutputUs
                    while (!WiFiVideoDecoder.drained(ended.get(), receiver.queue.depth, decoder.pendingFrames, decoder.holdingRecoverySlot)) {
                        if (cancelled()) throw IOException("Wi-Fi video cancelled")
                        val now = System.nanoTime() / 1000
                        if (!decoder.step(now)) Thread.sleep(1)
                        // Any submit or output counts as progress; idle time before the first frame is not a stall.
                        if (decoder.pendingFrames != seenPending || decoder.lastOutputUs != seenOutputUs) {
                            seenPending = decoder.pendingFrames
                            seenOutputUs = decoder.lastOutputUs
                            progressUs = now
                        }
                        if (decoder.pendingFrames != 0 && now - progressUs > STALL_US) {
                            throw IOException("Wi-Fi decoder stalled for two seconds")
                        }
                    }
                }
            } catch (error: Exception) {
                Log.e(TAG, "Wi-Fi decoder failed", error)
                failure.set(error)
                ready.completeExceptionally(error)
                try { closeMedia() } catch (closeError: IOException) { error.addSuppressed(closeError) }
            }
        }, "wifi-decoder")
        thread.start()
        val reporter = statsLink?.takeIf { limits.features and WiFiSessionProtocol.FEATURE_VIDEO_STATS != 0L }?.let {
            startStatsReporter(it, statsIntervalMs, receiver, active, linkSample)
        }
        try {
            ready.get(5, TimeUnit.SECONDS)
            link.sendControl(DECODER_READY)
            while (receiver.receive()) {
                failure.get()?.let { throw IOException("Wi-Fi decoder failed", it) }
            }
            ended.set(true)
            thread.join(3000)
            if (thread.isAlive) throw IOException("Wi-Fi decoder did not drain promptly")
            failure.get()?.let { throw IOException("Wi-Fi decode failed", it) }
        } catch (error: Exception) {
            try { closeMedia() } catch (closeError: IOException) { error.addSuppressed(closeError) }
            throw error
        } finally {
            reporter?.interrupt()
            ended.set(true)
            if (thread.isAlive) {
                thread.interrupt()
                thread.join(3000)
            }
            receiver.queue.close()
        }
    }

    /** Small records on a daemon thread so a slow upstream write never blocks receive or decode. */
    private fun startStatsReporter(
        link: WiFiControlLink,
        intervalMs: Long,
        receiver: WiFiVideoReceiver,
        active: AtomicReference<PlayerDecoder>,
        linkSample: () -> WiFiVideoStats.Link?
    ): Thread = Thread({
        val stats = WiFiVideoStats()
        var nextReportMs = System.nanoTime() / 1_000_000 + intervalMs
        try {
            while (!Thread.currentThread().isInterrupted) {
                Thread.sleep(minOf(intervalMs, KEYFRAME_POLL_MS))
                val queue = receiver.queue
                if (stats.keyframeNeeded(queue.discardEvents)) {
                    Log.i(TAG, "Requesting keyframe: queue overflows=${queue.overflowEvents} ageOuts=${queue.ageEvents} " +
                        "maxGapMs=${receiver.maxInterArrivalUs / 1000} bursts=${receiver.burstArrivals} " +
                        "decoderPending=${active.get()?.pendingFrames}")
                    link.sendControl(WiFiVideoStats.KEYFRAME_REQUEST)
                }
                val nowMs = System.nanoTime() / 1_000_000
                if (nowMs < nextReportMs) continue
                nextReportMs = nowMs + intervalMs
                val decoder = active.get()
                link.sendControl(stats.next(WiFiVideoStats.Counters(
                    received = receiver.received,
                    rendered = decoder?.renderReleased ?: 0L,
                    decodeDrops = decoder?.decodeDrops ?: 0L,
                    queueDropped = queue.dropped,
                    queued = queue.depth + (decoder?.pendingFrames ?: 0)
                ), intervalMs, true, runCatching(linkSample).getOrNull()))
            }
        } catch (_: InterruptedException) {
        } catch (error: IOException) {
            Log.d(TAG, "Wi-Fi stats reporter stopped: ${error.message}")
        }
    }, "wifi-video-stats").apply {
        isDaemon = true
        start()
    }
}
