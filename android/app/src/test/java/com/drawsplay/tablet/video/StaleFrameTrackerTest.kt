package com.drawsplay.tablet.video

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class StaleFrameTrackerTest {
    @Test
    fun steadyStreamRendersEveryFrame() {
        val sim = DecoderSim(depth = 0)
        sim.steady(frames = 600)
        assertEquals(0, sim.skipped)
    }

    @Test
    fun burstAfterStallSkipsToTheNewestFrames() {
        val sim = DecoderSim(depth = 0)
        sim.steady(frames = 180)
        sim.nowNs += 1_000_000_000L

        // A second of frames lands at once: eight fit the codec's input
        // buffers, the rest wait in the pending queue.
        val rendered = sim.drainBurst(pictures = 60, codecInputs = 8, outputSpacingNs = MS * 4)

        assertTrue("warm-up outputs render", rendered.take(StaleFrameTracker.CATCH_UP_OUTPUTS).all { it })
        assertTrue("the newest frames render", rendered.takeLast(2).all { it })
        assertFalse(rendered[rendered.size - 3])
        // Four warm-up outputs, one per 100 ms of catch-up (2), the newest two.
        assertEquals(8, rendered.count { it })
    }

    @Test
    fun longCatchUpStillShowsAFrameEveryHundredMs() {
        val sim = DecoderSim(depth = 0)
        sim.steady(frames = 180)
        sim.nowNs += 3_000_000_000L
        val start = sim.nowNs
        val spacing = MS * 2
        val rendered = sim.drainBurst(pictures = 300, codecInputs = 300, outputSpacingNs = spacing)

        val renderTimes = rendered.indices.filter { rendered[it] }.map { start + (it + 1) * spacing }
        val longestGap = renderTimes.zipWithNext { a, b -> b - a }.max()
        assertTrue("longest gap ${longestGap / MS} ms", longestGap <= MS * 100 + 2 * spacing)
        assertTrue(rendered.count { !it } > 250)
    }

    @Test
    fun decoderPipelineDepthIsNotMistakenForBacklog() {
        // A decoder that hands frame n back only once n+2 is queued.
        val sim = DecoderSim(depth = 2)
        sim.steady(frames = 300)
        sim.nowNs += 500 * MS

        val rendered = sim.drainBurst(pictures = 10, codecInputs = 10, outputSpacingNs = MS * 3)

        // Outputs 5..8 still have 4+ frames behind them beyond the usual two.
        assertEquals(
            listOf(true, true, true, true, false, false, false, false, true, true),
            rendered
        )
    }

    @Test
    fun decoderSlowerThanTheStreamKeepsEveryFrame() {
        val tracker = StaleFrameTracker()
        val arrivalNs = FRAME_NS
        val outputNs = MS * 22
        val queued = ArrayDeque<Long>()
        var arrivals = 0
        var nextOutputAt = MS * 5
        var skipped = 0
        var outputs = 0
        var now = 0L
        while (outputs < 150) {
            val nextArrivalAt = arrivals * arrivalNs
            if (nextArrivalAt <= nextOutputAt) {
                now = nextArrivalAt
                arrivals++
                tracker.onPictureQueued(arrivals.toLong())
                queued.addLast(arrivals.toLong())
            } else {
                now = nextOutputAt
                nextOutputAt += outputNs
                if (queued.isEmpty()) continue
                if (!tracker.shouldRender(queued.removeFirst(), 0, FRAME_NS, now)) skipped++
                outputs++
            }
        }
        assertTrue("backlog built up", queued.size > 30)
        assertEquals(0, skipped)
    }

    @Test
    fun unknownFrameRateNeverSkips() {
        val sim = DecoderSim(depth = 0)
        sim.steady(frames = 120)
        sim.nowNs += 1_000_000_000L
        val rendered = sim.drainBurst(pictures = 60, codecInputs = 60, outputSpacingNs = MS * 4, intervalNs = 0)
        assertTrue(rendered.all { it })
    }

    @Test
    fun decoderThatLosesTimestampsDisablesSkipping() {
        val tracker = StaleFrameTracker()
        var now = 0L
        var pts = 1_000L
        repeat(StaleFrameTracker.MAX_UNMATCHED_OUTPUTS + 2) {
            repeat(3) { tracker.onPictureQueued(pts++) }
            now += MS * 2
            tracker.shouldRender(0L, 40, FRAME_NS, now)
        }
        assertFalse(tracker.isEnabled)
        repeat(20) {
            now += MS * 2
            assertTrue(tracker.shouldRender(0L, 40, FRAME_NS, now))
        }
    }

    @Test
    fun unmatchedQueueGrowthDisablesSkipping() {
        val tracker = StaleFrameTracker()
        repeat(StaleFrameTracker.MAX_TRACKED_PICTURES + 1) { tracker.onPictureQueued(it.toLong() + 1) }
        assertFalse(tracker.isEnabled)
        assertTrue(tracker.shouldRender(1L, 100, FRAME_NS, MS))
    }

    /**
     * A decoder that returns each picture once [depth] newer ones are queued.
     * Pictures are numbered from 1; the number doubles as the input pts.
     */
    private class DecoderSim(private val depth: Int) {
        val tracker = StaleFrameTracker()
        var nowNs = 0L
        var skipped = 0
        private var lastQueued = 0L
        private var lastOutput = 0L

        fun steady(frames: Int) {
            repeat(depth) { queue() }
            repeat(frames) {
                nowNs += FRAME_NS
                queue()
                if (!output(pendingPictures = 0, intervalNs = FRAME_NS)) skipped++
            }
        }

        /**
         * Delivers [pictures] at once, [codecInputs] of them straight to the
         * codec and the rest pending, then decodes until only [depth]
         * remain, one output every [outputSpacingNs]. Each output frees an
         * input buffer for the next pending picture.
         */
        fun drainBurst(
            pictures: Int,
            codecInputs: Int,
            outputSpacingNs: Long,
            intervalNs: Long = FRAME_NS
        ): List<Boolean> {
            var pending = pictures
            repeat(minOf(codecInputs, pictures)) { queue(); pending-- }
            return List(pictures) {
                nowNs += outputSpacingNs
                val render = output(pending, intervalNs)
                if (pending > 0) { queue(); pending-- }
                render
            }
        }

        private fun queue() {
            lastQueued++
            tracker.onPictureQueued(lastQueued)
        }

        private fun output(pendingPictures: Int, intervalNs: Long): Boolean {
            lastOutput++
            return tracker.shouldRender(lastOutput, pendingPictures, intervalNs, nowNs)
        }
    }

    private companion object {
        const val MS = 1_000_000L
        const val FRAME_NS = 16_666_667L
    }
}
