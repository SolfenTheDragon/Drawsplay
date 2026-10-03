package com.drawsplay.tablet.input

import android.content.Context
import android.os.SystemClock
import android.util.AttributeSet
import android.view.MotionEvent
import android.view.View

/**
 * One captured input sample, ready for serialization on the input channel.
 * Coordinates are in view-local pixels; [width]/[height] carry the view size
 * so the host can normalize.
 */
data class InputEventPacket(
    val action: String,
    val x: Float,
    val y: Float,
    val pressure: Float,
    val tilt: Float,
    val orientation: Float,
    val buttons: Int,
    val toolType: String,
    val pointerId: Int,
    val pointerCount: Int,
    val width: Int,
    val height: Int,
    val timestampMs: Long
)

/**
 * Transparent overlay view that captures touch/stylus input at full digitizer
 * resolution and reports it via [onInputBatch].
 *
 * It is designed to sit directly on top of the video surface so that captured
 * coordinates map 1:1 onto displayed host pixels.
 *
 * Capture details:
 *  - Historical (intra-frame batched) move samples are expanded so full
 *    stroke resolution is preserved (pens report 240 Hz+ on many tablets).
 *  - Unbuffered dispatch is requested per gesture, removing display-refresh
 *    input batching latency.
 *  - Hover and stylus button events are forwarded from generic motion events.
 *  - Timestamps come from the input driver event time mapped to epoch ms,
 *    not the app delivery time.
 */
class InputPadView(context: Context, attrs: AttributeSet? = null) : View(context, attrs) {

    /** Called on the UI thread with each batch of captured samples. */
    var onInputBatch: ((List<InputEventPacket>) -> Unit)? = null

    override fun onTouchEvent(event: MotionEvent): Boolean {
        if (event.actionMasked == MotionEvent.ACTION_DOWN) {
            // Deliver input events as they arrive instead of batching them to
            // the display refresh; this removes up to a frame of input latency
            // for the duration of the gesture.
            requestUnbufferedDispatch(event)
        }

        val actionName = when (event.actionMasked) {
            MotionEvent.ACTION_DOWN -> "down"
            MotionEvent.ACTION_MOVE -> "move"
            MotionEvent.ACTION_UP -> "up"
            MotionEvent.ACTION_CANCEL -> "cancel"
            MotionEvent.ACTION_POINTER_DOWN -> "pointer_down"
            MotionEvent.ACTION_POINTER_UP -> "pointer_up"
            else -> "other"
        }

        val packets = if (event.actionMasked == MotionEvent.ACTION_MOVE) {
            collectMoveSamples(event, actionName)
        } else {
            val idx = event.actionIndex.coerceAtLeast(0)
            listOf(packetForPointer(event, actionName, idx))
        }

        onInputBatch?.invoke(packets)

        if (event.actionMasked == MotionEvent.ACTION_UP) {
            performClick()
        }
        return true
    }

    override fun performClick(): Boolean {
        super.performClick()
        return true
    }

    override fun onGenericMotionEvent(event: MotionEvent): Boolean {
        val actionName = when (event.actionMasked) {
            MotionEvent.ACTION_HOVER_ENTER -> "hover_enter"
            MotionEvent.ACTION_HOVER_MOVE -> "hover_move"
            MotionEvent.ACTION_HOVER_EXIT -> "hover_exit"
            MotionEvent.ACTION_BUTTON_PRESS -> "button_press"
            MotionEvent.ACTION_BUTTON_RELEASE -> "button_release"
            else -> return super.onGenericMotionEvent(event)
        }

        val packets = if (event.actionMasked == MotionEvent.ACTION_HOVER_MOVE) {
            collectMoveSamples(event, actionName)
        } else {
            listOf(packetForPointer(event, actionName, 0))
        }
        onInputBatch?.invoke(packets)
        return true
    }

    /**
     * Expands a move/hover-move event into packets for every pointer and every
     * historical sample. Android batches high-rate touch/pen samples into a
     * single MotionEvent; skipping history would silently discard most of the
     * stroke resolution.
     */
    private fun collectMoveSamples(event: MotionEvent, actionName: String): List<InputEventPacket> {
        val historySize = event.historySize
        val pointerCount = event.pointerCount
        val epochOffsetMs = System.currentTimeMillis() - SystemClock.uptimeMillis()
        val list = ArrayList<InputEventPacket>((historySize + 1) * pointerCount)
        for (h in 0 until historySize) {
            val sampleTimeMs = epochOffsetMs + event.getHistoricalEventTime(h)
            for (idx in 0 until pointerCount) {
                list.add(
                    InputEventPacket(
                        action = actionName,
                        x = event.getHistoricalX(idx, h),
                        y = event.getHistoricalY(idx, h),
                        pressure = event.getHistoricalPressure(idx, h),
                        tilt = event.getHistoricalAxisValue(MotionEvent.AXIS_TILT, idx, h),
                        orientation = event.getHistoricalOrientation(idx, h),
                        buttons = event.buttonState,
                        toolType = toToolType(event.getToolType(idx)),
                        pointerId = event.getPointerId(idx),
                        pointerCount = pointerCount,
                        width = width,
                        height = height,
                        timestampMs = sampleTimeMs
                    )
                )
            }
        }
        val currentTimeMs = epochOffsetMs + event.eventTime
        for (idx in 0 until pointerCount) {
            list.add(packetForPointer(event, actionName, idx, currentTimeMs))
        }
        return list
    }

    private fun packetForPointer(
        event: MotionEvent,
        actionName: String,
        idx: Int,
        timestampMs: Long = System.currentTimeMillis() - SystemClock.uptimeMillis() + event.eventTime
    ): InputEventPacket {
        return InputEventPacket(
            action = actionName,
            x = event.getX(idx),
            y = event.getY(idx),
            pressure = event.getPressure(idx),
            tilt = event.getAxisValue(MotionEvent.AXIS_TILT, idx),
            orientation = event.getOrientation(idx),
            buttons = event.buttonState,
            toolType = toToolType(event.getToolType(idx)),
            pointerId = event.getPointerId(idx),
            pointerCount = event.pointerCount,
            width = width,
            height = height,
            timestampMs = timestampMs
        )
    }

    private fun toToolType(toolType: Int): String {
        return when (toolType) {
            MotionEvent.TOOL_TYPE_STYLUS -> "stylus"
            MotionEvent.TOOL_TYPE_FINGER -> "finger"
            MotionEvent.TOOL_TYPE_MOUSE -> "mouse"
            MotionEvent.TOOL_TYPE_ERASER -> "eraser"
            else -> "unknown"
        }
    }
}

