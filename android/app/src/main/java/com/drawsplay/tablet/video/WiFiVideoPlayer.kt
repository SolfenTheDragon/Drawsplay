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
    fun step(nowUs: Long): Boolean
}

private class MediaCodecPlayerDecoder(private val decoder: WiFiVideoDecoder) : PlayerDecoder {
    override val pendingFrames get() = decoder.pendingFrames
    override val holdingRecoverySlot get() = decoder.holdingRecoverySlot
    override val lastOutputUs get() = decoder.lastOutputUs
    override fun step(nowUs: Long) = decoder.step(nowUs)
    override fun close() = decoder.close()
}

/** Plays one paired Wi-Fi video session: receive on the caller thread, decode on a dedicated thread. */
object WiFiVideoPlayer {
    private const val TAG = "WiFiVideoPlayer"
    private const val STALL_US = 2_000_000L
    private val DECODER_READY = byteArrayOf(7, 0, 0, 1)

    fun run(
        media: InputStream,
        closeMedia: () -> Unit,
        link: WiFiControlLink,
        limits: WiFiSessionProtocol.Hello,
        epoch: Long,
        surface: Surface,
        cancelled: () -> Boolean
    ) = run(media, closeMedia, link, limits, epoch, { config, queue ->
        MediaCodecPlayerDecoder(WiFiVideoDecoder(surface, config, queue, limits.maxAccessUnit))
    }, cancelled)

    fun run(
        media: InputStream,
        closeMedia: () -> Unit,
        link: WiFiControlLink,
        limits: WiFiSessionProtocol.Hello,
        epoch: Long,
        decoderFactory: (WiFiH264.Config, WiFiAccessUnitQueue) -> PlayerDecoder,
        cancelled: () -> Boolean
    ) {
        val receiver = WiFiVideoReceiver(media, limits, epoch)
        val ready = CompletableFuture<Unit>()
        val ended = AtomicBoolean(false)
        val failure = AtomicReference<Throwable>()
        val thread = Thread({
            try {
                decoderFactory(receiver.config, receiver.queue).use { decoder ->
                    ready.complete(Unit)
                    while (!WiFiVideoDecoder.drained(ended.get(), receiver.queue.depth, decoder.pendingFrames, decoder.holdingRecoverySlot)) {
                        if (cancelled()) throw IOException("Wi-Fi video cancelled")
                        val now = System.nanoTime() / 1000
                        if (!decoder.step(now)) Thread.sleep(1)
                        if (decoder.pendingFrames != 0 && now - decoder.lastOutputUs > STALL_US) {
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
            ended.set(true)
            if (thread.isAlive) {
                thread.interrupt()
                thread.join(3000)
            }
            receiver.queue.close()
        }
    }
}
