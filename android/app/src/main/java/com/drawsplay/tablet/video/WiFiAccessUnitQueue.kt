package com.drawsplay.tablet.video

import java.io.Closeable
import java.io.IOException

/** One network writer, one decoder reader. All storage is allocated at session setup. */
class WiFiAccessUnitQueue(maxAccessUnit: Int, slots: Int, budgetBytes: Int, private val maxAgeUs: Long) : Closeable {
    class Slot internal constructor(size: Int) {
        val bytes = ByteArray(size)
        var length = 0
            internal set
        var receivedUs = 0L
            internal set
        var restartDecoder = false
            internal set
        internal var state = FREE
        internal var order = 0L
    }

    init {
        require(maxAccessUnit in 65536..1024 * 1024 && slots in 2..4 &&
            budgetBytes in 1..8 * 1024 * 1024 &&
            maxAccessUnit.toLong() * slots <= budgetBytes && maxAgeUs in 1..500000)
    }

    private val pool = Array(slots) { Slot(maxAccessUnit) }
    @Suppress("PLATFORM_CLASS_MAPPED_TO_KOTLIN")
    private val monitor = this as java.lang.Object
    val storageBytes = maxAccessUnit * slots
    @Volatile var dropped = 0L
        private set
    @Volatile var overflowEvents = 0L
        private set
    @Volatile var ageEvents = 0L
        private set
    /** Queue discards that left the decoder waiting for an IDR. */
    val discardEvents: Long get() = overflowEvents + ageEvents
    private var nextOrder = 0L
    private var needsIdr = true
    private var closed = false
    private var writer: Slot? = null
    private var reader: Slot? = null

    val depth: Int
        @Synchronized get() {
            var count = 0
            for (slot in pool) if (slot.state == READY) count++
            return count
        }

    /**
     * Waits up to [waitUs] for the decoder to free a slot, so a burst after a
     * short link stall applies backpressure instead of discarding reference
     * pictures; only a decoder that frees nothing in time forces an IDR.
     */
    @Synchronized
    fun acquire(waitUs: Long = 0): Slot {
        ensureOpen()
        if (writer != null) throw IOException("Wi-Fi AU writer still owns a slot")
        var available = freeSlot()
        if (available == null && waitUs > 0) {
            val deadlineNs = System.nanoTime() + waitUs * 1000
            while (available == null) {
                val remainingNs = deadlineNs - System.nanoTime()
                if (remainingNs <= 0) break
                try {
                    monitor.wait(remainingNs / 1_000_000, (remainingNs % 1_000_000).toInt())
                } catch (_: InterruptedException) {
                    Thread.currentThread().interrupt()
                    throw IOException("Wi-Fi AU writer interrupted")
                }
                ensureOpen()
                available = freeSlot()
            }
        }
        if (available == null) {
            overflowEvents++
            discardReady()
            needsIdr = true
            available = freeSlot()
        }
        val selected = available ?: throw IOException("Wi-Fi AU pool exhausted by invalid ownership")
        selected.state = WRITING
        selected.length = 0
        selected.restartDecoder = false
        writer = selected
        return selected
    }

    /** Loss of any reference picture suppresses all dependent AUs until a fresh IDR. */
    @Synchronized
    fun publish(slot: Slot, length: Int, idr: Boolean, receivedUs: Long): Boolean {
        ensureOpen()
        if (writer !== slot || slot.state != WRITING || length !in 1..slot.bytes.size || receivedUs < 0) {
            throw IOException("Invalid Wi-Fi AU writer ownership, length or timestamp")
        }
        writer = null
        if (needsIdr && !idr) {
            slot.state = FREE
            dropped++
            return false
        }
        slot.length = length
        slot.receivedUs = receivedUs
        slot.restartDecoder = needsIdr
        slot.order = ++nextOrder
        slot.state = READY
        needsIdr = false
        return true
    }

    @Synchronized
    fun abandon(slot: Slot) {
        if (writer !== slot || slot.state != WRITING) throw IOException("Invalid Wi-Fi AU writer release")
        writer = null
        slot.state = FREE
    }

    @Synchronized
    fun poll(nowUs: Long): Slot? {
        ensureOpen()
        if (reader != null) throw IOException("Wi-Fi AU decoder still owns a slot")
        var oldest: Slot? = null
        for (slot in pool) if (slot.state == READY && (oldest == null || slot.order < oldest.order)) oldest = slot
        val selected = oldest ?: return null
        // The producer can publish after the consumer sampled its clock.
        if (nowUs >= selected.receivedUs && nowUs - selected.receivedUs >= maxAgeUs) {
            ageEvents++
            discardReady()
            needsIdr = true
            return null
        }
        selected.state = READING
        reader = selected
        return selected
    }

    @Synchronized
    fun release(slot: Slot) {
        if (reader !== slot || slot.state != READING) throw IOException("Invalid Wi-Fi AU decoder release")
        reader = null
        slot.state = FREE
        monitor.notifyAll()
    }

    @Synchronized
    override fun close() {
        closed = true
        discardReady()
        needsIdr = true
        monitor.notifyAll()
    }

    private fun freeSlot(): Slot? {
        for (slot in pool) if (slot.state == FREE) return slot
        return null
    }

    private fun ensureOpen() {
        if (closed) throw IOException("Wi-Fi AU queue is closed")
    }

    private fun discardReady() {
        for (slot in pool) if (slot.state == READY) {
            slot.state = FREE
            dropped++
        }
        monitor.notifyAll()
    }

    companion object {
        private const val FREE = 0
        private const val WRITING = 1
        private const val READY = 2
        private const val READING = 3
    }
}
