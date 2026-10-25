package com.drawsplay.tablet.video

import org.junit.Assert.assertEquals
import org.junit.Test

class FirstPictureSignalTest {
    @Test
    fun creatingConnectionWithoutRenderedOutputDoesNotSignal() {
        var pictures = 0
        FirstPictureSignal(onPictureReady = { pictures++ })
        assertEquals(0, pictures)
    }

    @Test
    fun renderedPicturesSignalOnlyOncePerConnection() {
        var pictures = 0
        val signal = FirstPictureSignal(onPictureReady = { pictures++ })
        repeat(3) { signal.signal() }
        assertEquals(1, pictures)
    }

    @Test
    fun newConnectionCanSignalAgain() {
        var pictures = 0
        val first = FirstPictureSignal(onPictureReady = { pictures++ })
        first.signal()
        first.close()
        val second = FirstPictureSignal(onPictureReady = { pictures++ })
        second.signal()
        assertEquals(2, pictures)
    }

    @Test
    fun queuedRenderCallbackFromClosedConnectionDoesNotSignal() {
        var pictures = 0
        val signal = FirstPictureSignal(onPictureReady = { pictures++ })
        signal.close()
        signal.signal()
        assertEquals(0, pictures)
    }

    @Test
    fun renderCallbackFromOldGenerationDoesNotSignal() {
        var generation = 1
        var pictures = 0
        val signal = FirstPictureSignal({ generation == 1 }) { pictures++ }
        generation = 2
        signal.signal()
        assertEquals(0, pictures)
    }

    @Test
    fun queuedDeliveryIsSuppressedWhenConnectionClosesBeforeDispatch() {
        val queued = mutableListOf<() -> Unit>()
        var pictures = 0
        val signal = FirstPictureSignal(dispatch = { queued += it }) { pictures++ }
        signal.signal()
        assertEquals(0, pictures)
        signal.close()
        queued.forEach { it() }
        assertEquals(0, pictures)
    }

    @Test
    fun queuedDeliveryRechecksGenerationAndOnlyDispatchesOnce() {
        val queued = mutableListOf<() -> Unit>()
        var generation = 1
        var pictures = 0
        val old = FirstPictureSignal({ generation == 1 }, { queued += it }) { pictures++ }
        repeat(3) { old.signal() }
        assertEquals(1, queued.size)
        generation = 2
        queued.forEach { it() }
        assertEquals(0, pictures)
        queued.clear()
        val current = FirstPictureSignal({ generation == 2 }, { queued += it }) { pictures++ }
        repeat(3) { current.signal() }
        queued.forEach { it() }
        assertEquals(1, pictures)
    }
}
