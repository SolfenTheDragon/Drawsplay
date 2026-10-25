package com.drawsplay.tablet.video

internal class FirstPictureSignal(
    private val isCurrent: () -> Boolean = { true },
    private val dispatch: (() -> Unit) -> Unit = { it() },
    private val onPictureReady: () -> Unit
) {
    private var active = true
    private var signalled = false

    @Synchronized
    fun signal() {
        if (!active || signalled || !isCurrent()) return
        signalled = true
        dispatch { deliver() }
    }

    @Synchronized
    private fun deliver() {
        if (active && isCurrent()) onPictureReady()
    }

    @Synchronized
    fun close() {
        active = false
    }
}
