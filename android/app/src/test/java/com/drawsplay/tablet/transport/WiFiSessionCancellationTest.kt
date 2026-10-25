package com.drawsplay.tablet.transport

import java.io.Closeable
import java.io.IOException
import java.net.ServerSocket
import java.net.Socket
import java.net.SocketException
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference
import org.junit.Assert.*
import org.junit.Test

class WiFiSessionCancellationTest {
    @Test
    fun cancellationClosesListeningAndPendingHandshakeSockets() {
        val cancellation = WiFiSessionCancellation()
        val listener = cancellation.track(ServerSocket(0))
        val pending = cancellation.track(Socket())
        cancellation.close()
        assertTrue(listener.isClosed)
        assertTrue(pending.isClosed)
        assertFalse(cancellation.isActive)
    }

    @Test
    fun resourceCreatedAfterCancellationIsClosedAndRejected() {
        val cancellation = WiFiSessionCancellation()
        cancellation.close()
        val socket = Socket()
        assertThrows(IOException::class.java) { cancellation.track(socket) }
        assertTrue(socket.isClosed)
    }

    @Test
    fun cancelledSessionCannotPublishAnOldHostConnection() {
        val old = WiFiSessionCancellation()
        val selector = HostEndpointSelector()
        val replacement = Socket()
        val stale = Socket()
        old.close()
        try {
            selector.updatePairedControl(replacement)
            assertFalse(old.publish { selector.updatePairedControl(stale) })
            assertSame(replacement, selector.inputCandidates().last().pairedSocket)
        } finally {
            replacement.close()
            stale.close()
        }
    }

    @Test
    fun releasingFinishedSessionDoesNotLeaveItInReconnectCleanup() {
        val cancellation = WiFiSessionCancellation()
        var closes = 0
        val session = cancellation.track(Closeable { closes++ })
        cancellation.release(session)
        cancellation.close()
        assertEquals(1, closes)
    }

    @Test
    fun failedCloseStillCancelsAndClosesOtherResources() {
        val cancellation = WiFiSessionCancellation()
        cancellation.track(Closeable { throw IOException("close failed") })
        val socket = cancellation.track(Socket())
        assertThrows(IOException::class.java) { cancellation.close() }
        assertTrue(socket.isClosed)
        assertFalse(cancellation.publish { fail("cancelled session published") })
    }

    @Test
    fun stoppingReconnectUnblocksPendingAcceptAndClosesNestedSession() {
        val reconnect = WiFiSessionCancellation()
        val session = reconnect.track(WiFiSessionCancellation())
        val listener = session.track(ServerSocket(0))
        val entered = CountDownLatch(1)
        val finished = CountDownLatch(1)
        val failure = AtomicReference<Throwable?>()
        val worker = Thread {
            entered.countDown()
            try {
                listener.accept().use { failure.set(AssertionError("Unexpected connection")) }
            } catch (_: SocketException) {
                // Expected: closing the listener cancels a blocking accept.
            } catch (error: Throwable) {
                failure.set(error)
            } finally {
                finished.countDown()
            }
        }
        worker.start()
        try {
            assertTrue(entered.await(3, TimeUnit.SECONDS))
            reconnect.close()
            assertTrue("Pending accept was not cancelled", finished.await(3, TimeUnit.SECONDS))
            assertFalse(session.isActive)
            assertTrue(listener.isClosed)
            assertNull(failure.get())
        } finally {
            listener.close()
            worker.join(3000)
        }
    }
}
