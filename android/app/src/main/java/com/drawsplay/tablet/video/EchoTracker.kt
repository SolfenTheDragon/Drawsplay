package com.drawsplay.tablet.video

import java.util.concurrent.atomic.AtomicInteger

/**
 * Measures input-to-frame echo latency. Every input packet carries a
 * sequence number; the host reports the newest sequence it had injected when
 * each frame was captured (video frame-meta packet), so the time from send to
 * the first frame carrying that sequence is a close upper bound on the
 * input-to-photon path minus the panel scan-out.
 */
object EchoTracker {
    private const val RING = 256
    private const val SAMPLES = 32
    private const val FRESH_NS = 2_000_000_000L

    private val counter = AtomicInteger(0)
    private val sentNs = LongArray(RING)
    private val sampleNs = LongArray(SAMPLES)
    private val sampleAt = LongArray(SAMPLES)
    private var sampleNext = 0
    private var lastSeq = 0

    /** Allocates the next sequence (never 0) and records its send time. */
    fun nextSeq(nowNs: Long = System.nanoTime()): Int {
        val seq = counter.incrementAndGet().let { if (it == 0) counter.incrementAndGet() else it }
        synchronized(this) { sentNs[seq and (RING - 1)] = nowNs }
        return seq
    }

    /** Called when a frame whose capture saw input [seq] arrives. */
    fun onFrameMeta(seq: Int, nowNs: Long = System.nanoTime()) {
        if (seq == 0) return
        synchronized(this) {
            if (seq == lastSeq) return
            val newest = counter.get()
            if (newest - seq !in 0 until RING) return
            lastSeq = seq
            val sent = sentNs[seq and (RING - 1)]
            if (sent == 0L || nowNs < sent) return
            sampleNs[sampleNext] = nowNs - sent
            sampleAt[sampleNext] = nowNs
            sampleNext = (sampleNext + 1) % SAMPLES
        }
    }

    /** Median echo latency over samples from the last two seconds, 0 if none. */
    fun medianMs(nowNs: Long = System.nanoTime()): Double = synchronized(this) {
        val fresh = ArrayList<Long>(SAMPLES)
        for (i in 0 until SAMPLES) {
            if (sampleAt[i] != 0L && nowNs - sampleAt[i] <= FRESH_NS) fresh.add(sampleNs[i])
        }
        if (fresh.isEmpty()) return 0.0
        fresh.sort()
        fresh[fresh.size / 2] / 1_000_000.0
    }
}
