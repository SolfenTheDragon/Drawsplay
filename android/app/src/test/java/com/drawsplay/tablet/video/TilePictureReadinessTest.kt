package com.drawsplay.tablet.video

import org.junit.Assert.assertEquals
import org.junit.Test

class TilePictureReadinessTest {
    private class Publisher : TilePicturePublisher {
        var published: () -> Unit = {}
        override fun onFullBegin(width: Int, height: Int, tileSize: Int) {}
        override fun onTile(tx: Int, ty: Int, w: Int, h: Int, argb: IntArray) {}
        override fun onFullEnd() {}
        override fun onFullEnd(onPictureReady: () -> Unit) { published = onPictureReady }
    }

    @Test
    fun fullRefreshWaitsForDrawingInsteadOfSignallingAtDecodeCompletion() {
        var pictures = 0
        val publisher = Publisher()
        val sink = PictureReadyTileSink(publisher, FirstPictureSignal(onPictureReady = { pictures++ }))
        sink.onFullBegin(1, 1, 1)
        sink.onTile(0, 0, 1, 1, intArrayOf(-1))
        sink.onPacketDone()
        assertEquals(0, pictures)
        sink.onFullEnd()
        assertEquals(0, pictures)
        publisher.published()
        publisher.published()
        assertEquals(1, pictures)
    }

    @Test
    fun staleTileDrawingCannotSignalForDisconnectedConnection() {
        var pictures = 0
        val publisher = Publisher()
        val signal = FirstPictureSignal(onPictureReady = { pictures++ })
        val sink = PictureReadyTileSink(publisher, signal)
        sink.onFullEnd()
        signal.close()
        publisher.published()
        assertEquals(0, pictures)
    }

    @Test
    fun ordinarySinkCannotClaimPictureReadinessWithoutPublicationAcknowledgement() {
        var pictures = 0
        val sink = PictureReadyTileSink(object : TileDecoder.Sink {
            override fun onFullBegin(width: Int, height: Int, tileSize: Int) {}
            override fun onTile(tx: Int, ty: Int, w: Int, h: Int, argb: IntArray) {}
            override fun onFullEnd() {}
        }, FirstPictureSignal(onPictureReady = { pictures++ }))
        sink.onFullEnd()
        assertEquals(0, pictures)
    }
}
