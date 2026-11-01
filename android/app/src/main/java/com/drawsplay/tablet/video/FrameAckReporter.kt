package com.drawsplay.tablet.video

import android.util.Log
import java.io.OutputStream

/**
 * Sends cumulative frame acknowledgements upstream on the video socket as
 * `{"type":"ack","frames":N}` lines, N being the pictures the decoder has
 * finished with ([VideoDecoder.finishedFrames]).
 *
 * The host's native capture helpers keep at most a few frames beyond this
 * count in flight and encode only the newest frame when the window opens, so
 * neither the link nor the decoder can build a backlog (the flow control
 * SuperDisplay uses). Acks are coalesced on this thread: decoder callbacks
 * only flag that the count moved and never block on the socket.
 *
 * [writeLock] must be held by every writer of [output].
 */
class FrameAckReporter(
    private val output: OutputStream,
    private val writeLock: Any,
    private val frames: () -> Long
) {
    private val wake = Object()
    private var pending = false
    private var keyframeWanted = false
    private var lastKeyframeRequestNs = 0L
    private val thread = Thread(::run, "video-acks").apply { isDaemon = true }

    fun start() = thread.start()

    fun stop() = thread.interrupt()

    /** Called whenever the finished-frame count grows; never blocks. */
    fun notifyFinished() {
        synchronized(wake) {
            pending = true
            wake.notify()
        }
    }

    /**
     * Asks the host for a keyframe (`{"type":"keyframe"}`); never blocks.
     * The host's helpers send keyframes only on request.
     */
    fun requestKeyframe() {
        synchronized(wake) {
            keyframeWanted = true
            wake.notify()
        }
    }

    private fun run() {
        var sent = 0L
        try {
            while (!Thread.currentThread().isInterrupted) {
                var keyframe: Boolean
                synchronized(wake) {
                    while (!pending && !keyframeWanted) wake.wait()
                    pending = false
                    keyframe = keyframeWanted
                    keyframeWanted = false
                }
                if (keyframe) {
                    val now = System.nanoTime()
                    // The host spaces keyframes itself; repeats only add noise.
                    if (now - lastKeyframeRequestNs >= KEYFRAME_REQUEST_SPACING_NS) {
                        lastKeyframeRequestNs = now
                        write(KEYFRAME_LINE)
                    }
                }
                val n = frames()
                if (n <= sent) continue
                sent = n
                write(ackLine(n))
            }
        } catch (_: InterruptedException) {
        } catch (e: Exception) {
            Log.d(TAG, "Ack reporter stopped: ${e.message}")
        }
    }

    private fun write(line: String) {
        val bytes = line.toByteArray(Charsets.UTF_8)
        synchronized(writeLock) {
            output.write(bytes)
            output.flush()
        }
    }

    companion object {
        private const val TAG = "FrameAckReporter"
        const val KEYFRAME_LINE = "{\"type\":\"keyframe\"}\n"
        private const val KEYFRAME_REQUEST_SPACING_NS = 500_000_000L

        /** The upstream ack line for a cumulative picture count. */
        fun ackLine(frames: Long): String = "{\"type\":\"ack\",\"frames\":$frames}\n"
    }
}
