package com.drawsplay.tablet.video

/** A tile sink acknowledging full-refresh publication, not just decode completion. */
interface TilePicturePublisher : TileDecoder.Sink {
    fun onFullEnd(onPictureReady: () -> Unit)
}

internal class PictureReadyTileSink(
    private val delegate: TileDecoder.Sink,
    private val pictureReady: FirstPictureSignal
) : TileDecoder.Sink by delegate {
    override fun onFullEnd() {
        if (delegate is TilePicturePublisher) {
            delegate.onFullEnd(pictureReady::signal)
        } else {
            delegate.onFullEnd()
        }
    }
}
