package com.drawsplay.tablet.video

/**
 * Holds the newest decoded output of one drain pass. Older outputs ready in
 * the same pass are superseded (never shown); the newest is always shown,
 * however late, because dropping it would only freeze the previous picture.
 */
class WiFiNewestOutput {
    private var held = -1

    /** Holds [index]; returns the output it supersedes, or -1. */
    fun offer(index: Int): Int {
        val previous = held
        held = index
        return previous
    }

    /** Returns and clears the held output, or -1. */
    fun takeNewest(): Int {
        val newest = held
        held = -1
        return newest
    }
}
