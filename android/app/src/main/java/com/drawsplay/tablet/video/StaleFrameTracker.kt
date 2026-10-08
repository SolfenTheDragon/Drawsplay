package com.drawsplay.tablet.video

/**
 * Decides which decoded frames are still worth showing in latency render
 * mode.
 *
 * Any stall upstream of the decoder (a Wi-Fi roam, a USB or adb hiccup, a
 * busy host) leaves the frames captured meanwhile buffered on the way; when
 * the stall clears they arrive together and decode back to back. Rendering
 * each one the moment it decodes replays the stall in fast forward. A frame
 * whose successors are already queued for decode is stale before it could
 * reach the screen, so it is still decoded (later pictures predict from it)
 * but released without rendering, and the picture jumps straight to the
 * newest frame instead.
 *
 * "Newer" counts picture starts queued to the codec after the output's own
 * picture plus those still waiting for an input buffer. A frame is stale when
 * that count exceeds the decoder's steady-state depth (the smallest count seen
 * over the current and previous baseline windows) by at least [staleMargin].
 * Measuring against the baseline keeps a decoder that always holds a picture
 * or two internally rendering every frame. A margin of two leaves ordinary
 * jitter, two frames arriving together, rendering exactly as before.
 *
 * During a long catch-up a frame is still rendered every [maxSkipStreakNs],
 * so the screen never looks frozen while the backlog drains.
 *
 * Skipping only applies while the decoder is catching up: outputs arriving
 * faster than the stream's frame interval. A decoder that is merely too slow
 * for the stream also accumulates a backlog, but skipping its renders would
 * not speed decoding up; it would only cut the visible frame rate. Its
 * backlog is cleared by keyframe skips in [VideoDecoder] instead.
 *
 * Correlation relies on the decoder handing back each input's
 * presentationTimeUs, which MediaCodec requires; a decoder that does not
 * (outputs that stop matching queued pictures) disables skipping.
 *
 * Not thread-safe: the caller serializes every call.
 */
internal class StaleFrameTracker(
    private val staleMargin: Int = DEFAULT_STALE_MARGIN,
    private val maxSkipStreakNs: Long = DEFAULT_MAX_SKIP_STREAK_NS,
    private val baselineWindowNs: Long = DEFAULT_BASELINE_WINDOW_NS
) {
    // Presentation times of picture starts queued to the codec and not yet
    // matched by an output, oldest first. Strictly increasing.
    private val queuedPictures = ArrayDeque<Long>()
    private var enabled = true
    private var unmatchedOutputs = 0

    private var windowOpen = false
    private var windowStartNs = 0L
    private var windowMin = NO_SAMPLE
    private var previousWindowMin = NO_SAMPLE

    private var skipping = false
    private var skipStreakStartNs = 0L

    // Times of the most recent outputs, oldest at [outputTimesNext] once full.
    private val recentOutputTimesNs = LongArray(CATCH_UP_OUTPUTS)
    private var recentOutputCount = 0
    private var outputTimesNext = 0

    /** Records a picture start queued to the codec with [ptsUs]. */
    fun onPictureQueued(ptsUs: Long) {
        if (!enabled) return
        queuedPictures.addLast(ptsUs)
        if (queuedPictures.size > MAX_TRACKED_PICTURES) disable()
    }

    /**
     * Returns whether the decoded frame carrying [outputPtsUs] should be
     * rendered. [pendingPictures] counts picture starts still waiting for an
     * input buffer; [frameIntervalNs] is the stream's frame interval (0 when
     * unknown, which never skips). Call for every output, rendered or not,
     * so queued pictures are matched off.
     */
    fun shouldRender(
        outputPtsUs: Long,
        pendingPictures: Int,
        frameIntervalNs: Long,
        nowNs: Long
    ): Boolean {
        if (!enabled) return true
        val before = queuedPictures.size
        // This output's own picture plus any older ones the decoder dropped.
        while (queuedPictures.isNotEmpty() && queuedPictures.first() <= outputPtsUs) {
            queuedPictures.removeFirst()
        }
        if (before > 0 && queuedPictures.size == before) {
            if (++unmatchedOutputs >= MAX_UNMATCHED_OUTPUTS) {
                disable()
                return true
            }
        } else {
            unmatchedOutputs = 0
        }

        val catchingUp = recordOutput(nowNs, frameIntervalNs)
        val newer = queuedPictures.size + pendingPictures.coerceAtLeast(0)
        val baseline = rollBaseline(nowNs)
        if (newer < windowMin) windowMin = newer
        if (!catchingUp || newer - baseline < staleMargin) {
            skipping = false
            return true
        }
        if (!skipping) {
            skipping = true
            skipStreakStartNs = nowNs
            return false
        }
        if (nowNs - skipStreakStartNs >= maxSkipStreakNs) {
            skipStreakStartNs = nowNs
            return true
        }
        return false
    }

    /** False once skipping was disabled because outputs stopped matching. */
    val isEnabled: Boolean
        get() = enabled

    /**
     * Records an output at [nowNs]; true when the last [CATCH_UP_OUTPUTS]
     * output intervals were together shorter than the stream would take, by
     * the [CATCH_UP_SPEED_NUM]/[CATCH_UP_SPEED_DEN] factor.
     */
    private fun recordOutput(nowNs: Long, frameIntervalNs: Long): Boolean {
        val full = recentOutputCount == CATCH_UP_OUTPUTS
        val oldestNs = recentOutputTimesNs[outputTimesNext]
        recentOutputTimesNs[outputTimesNext] = nowNs
        outputTimesNext = (outputTimesNext + 1) % CATCH_UP_OUTPUTS
        if (!full) recentOutputCount++
        if (!full || frameIntervalNs <= 0) return false
        val spanNs = nowNs - oldestNs
        return spanNs * CATCH_UP_SPEED_DEN < CATCH_UP_OUTPUTS * frameIntervalNs * CATCH_UP_SPEED_NUM
    }

    // Baseline from samples before this one, so a burst never sets its own.
    private fun rollBaseline(nowNs: Long): Int {
        if (!windowOpen) {
            windowOpen = true
            windowStartNs = nowNs
        } else if (nowNs - windowStartNs >= baselineWindowNs) {
            previousWindowMin = windowMin
            windowMin = NO_SAMPLE
            windowStartNs = nowNs
        }
        val min = minOf(windowMin, previousWindowMin)
        return if (min == NO_SAMPLE) 0 else min
    }

    private fun disable() {
        enabled = false
        queuedPictures.clear()
    }

    companion object {
        const val DEFAULT_STALE_MARGIN = 2
        const val DEFAULT_MAX_SKIP_STREAK_NS = 100_000_000L
        const val DEFAULT_BASELINE_WINDOW_NS = 2_000_000_000L

        // Far beyond the decoder's input buffers plus the pending queue's
        // bound: only reachable when outputs never match queued pictures.
        internal const val MAX_TRACKED_PICTURES = 1024
        internal const val MAX_UNMATCHED_OUTPUTS = 8

        // Catching up: the last four outputs came at least 4/3 faster than
        // the stream's frame rate. Several intervals smooth out decoders
        // that hand frames back in pairs.
        internal const val CATCH_UP_OUTPUTS = 4
        private const val CATCH_UP_SPEED_NUM = 3L
        private const val CATCH_UP_SPEED_DEN = 4L
        private const val NO_SAMPLE = Int.MAX_VALUE
    }
}
