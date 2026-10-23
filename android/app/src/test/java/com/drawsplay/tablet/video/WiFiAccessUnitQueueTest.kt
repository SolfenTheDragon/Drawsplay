package com.drawsplay.tablet.video

import java.io.IOException
import org.junit.Assert.*
import org.junit.Test

class WiFiAccessUnitQueueTest {
    @Test
    fun buffersAreReusedAndLeasedReaderCannotBeOverwrittenOnOverflow() {
        val queue = WiFiAccessUnitQueue(65536, 2, 131072, 50000)
        val first = queue.acquire()
        first.bytes[0] = 42
        assertTrue(queue.publish(first, 10, true, 100))
        val reading = queue.poll(100)!!
        val second = queue.acquire()
        assertNotSame(reading.bytes, second.bytes)
        assertTrue(queue.publish(second, 12, false, 200))
        val replacement = queue.acquire()
        assertSame(second, replacement)
        assertEquals(42, reading.bytes[0].toInt())
        assertFalse(queue.publish(replacement, 12, false, 300))
        assertEquals(0, queue.depth)
        val recovery = queue.acquire()
        assertTrue(queue.publish(recovery, 15, true, 400))
        assertTrue(recovery.restartDecoder)
        queue.release(reading)
        assertSame(recovery, queue.poll(400))
        queue.release(recovery)
        assertEquals(2L, queue.dropped)
        assertEquals(131072, queue.storageBytes)
    }

    @Test
    fun staleReferenceDropsDependentPicturesUntilFreshIdr() {
        val queue = WiFiAccessUnitQueue(65536, 3, 196608, 50000)
        val first = queue.acquire()
        assertFalse(queue.publish(first, 5, false, 100))
        assertSame(first, queue.acquire())
        assertTrue(queue.publish(first, 5, true, 200))
        val next = queue.acquire()
        assertTrue(queue.publish(next, 5, false, 300))
        assertNull(queue.poll(50200)) // Exact age bound is already stale.
        val dependent = queue.acquire()
        assertFalse(queue.publish(dependent, 5, false, 50300))
        val fresh = queue.acquire()
        assertTrue(queue.publish(fresh, 5, true, 50400))
        val ready = queue.poll(50401)!!
        assertSame(fresh, ready)
        assertTrue(ready.restartDecoder)
        assertEquals(50400L, ready.receivedUs)
        queue.release(ready)
        assertEquals(4L, queue.dropped)
        assertEquals(0, queue.depth)
    }

    @Test
    fun invalidOwnershipAndBoundsFailWithoutGrowingStorage() {
        for (create in listOf<() -> Unit>(
            { WiFiAccessUnitQueue(65536, 5, 8 * 1024 * 1024, 50000) },
            { WiFiAccessUnitQueue(1024 * 1024, 4, 3 * 1024 * 1024, 50000) },
            { WiFiAccessUnitQueue(Int.MAX_VALUE, 4, 8 * 1024 * 1024, 50000) },
            { WiFiAccessUnitQueue(65536, 2, 131072, 0) }
        )) {
            assertThrows(IllegalArgumentException::class.java) { create() }
        }
        val queue = WiFiAccessUnitQueue(65536, 2, 131072, 50000)
        val writer = queue.acquire()
        assertThrows(IOException::class.java) { queue.acquire() }
        assertThrows(IOException::class.java) { queue.publish(writer, 65537, true, 100) }
        queue.abandon(writer)
        assertThrows(IOException::class.java) { queue.release(writer) }
        val other = WiFiAccessUnitQueue(65536, 2, 131072, 50000).acquire()
        assertThrows(IOException::class.java) { queue.publish(other, 5, true, 100) }
        queue.close()
        assertThrows(IOException::class.java) { queue.acquire() }
        assertThrows(IOException::class.java) { queue.poll(100) }
    }

    @Test
    fun steadyStateCyclesUseOnlyThePreallocatedSlots() {
        val queue = WiFiAccessUnitQueue(65536, 2, 131072, 50000)
        val known = arrayOf(queue.acquire())
        queue.abandon(known[0])
        repeat(10000) { index ->
            val slot = queue.acquire()
            assertSame(known[0], slot)
            assertTrue(queue.publish(slot, 10, index == 0, index.toLong() + 1))
            assertSame(slot, queue.poll(index.toLong() + 1))
            queue.release(slot)
        }
        assertEquals(0L, queue.dropped)
    }

    @Test
    fun producerPublishingAfterConsumerClockSampleIsNotMistakenForStaleVideo() {
        val queue = WiFiAccessUnitQueue(65536, 2, 131072, 33333)
        val writer = queue.acquire()
        queue.publish(writer, 10, true, 101)
        val taken = queue.poll(100)
        assertSame("A stale clock sample is not a stale reference picture", writer, taken)
        queue.release(writer)
        assertEquals(0L, queue.dropped)
    }

    @Test
    fun warmedQueueOperationsAllocateNoPerFrameObjects() {
        val queue = WiFiAccessUnitQueue(65536, 2, 131072, 50000)
        fun cycle() {
            val slot = queue.acquire()
            queue.publish(slot, 10, true, 1)
            queue.poll(1)
            queue.release(slot)
        }
        repeat(100000) { cycle() }
        // Android's compile classpath omits JDK management APIs; this runs only on the test JVM.
        val bean = Class.forName("java.lang.management.ManagementFactory").getMethod("getThreadMXBean").invoke(null)
        val type = Class.forName("com.sun.management.ThreadMXBean")
        type.getMethod("setThreadAllocatedMemoryEnabled", Boolean::class.javaPrimitiveType).invoke(bean, true)
        val allocatedBytes = type.getMethod("getThreadAllocatedBytes", Long::class.javaPrimitiveType)
        val arguments = arrayOf<Any>(Thread.currentThread().id)
        repeat(100) { allocatedBytes.invoke(bean, *arguments) }
        val before = allocatedBytes.invoke(bean, *arguments) as Long
        repeat(100000) { cycle() }
        val allocated = (allocatedBytes.invoke(bean, *arguments) as Long) - before
        assertTrue("Per-frame queue allocation: $allocated bytes; only fixed reflection measurement overhead is allowed", allocated <= 128)
    }
}
