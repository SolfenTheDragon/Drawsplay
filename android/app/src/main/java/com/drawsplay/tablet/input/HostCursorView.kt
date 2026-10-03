package com.drawsplay.tablet.input

import android.content.Context
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.graphics.Path
import android.util.AttributeSet
import android.view.View
import kotlin.math.max
import kotlin.math.min

/**
 * Locally rendered host mouse cursor.
 *
 * Windows Desktop Duplication can deliver cursor-only changes at a much
 * lower cadence than video damage. The host therefore sends normalized
 * pointer coordinates over the low-latency input channel and this view draws
 * the cursor independently of the decoded desktop frame.
 */
class HostCursorView @JvmOverloads constructor(
    context: Context,
    attrs: AttributeSet? = null
) : View(context, attrs) {

    private val density = resources.displayMetrics.density
    private val cursorWidth = 22f * density
    private val cursorHeight = 30f * density
    private val cursorPath = Path().apply {
        moveTo(0f, 0f)
        lineTo(0f, 24f * density)
        lineTo(6f * density, 18f * density)
        lineTo(11f * density, 29f * density)
        lineTo(15f * density, 27f * density)
        lineTo(10f * density, 16f * density)
        lineTo(22f * density, 16f * density)
        close()
    }
    private val fill = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = Color.WHITE
        style = Paint.Style.FILL
    }
    private val outline = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = Color.BLACK
        style = Paint.Style.STROKE
        strokeWidth = 2f * density
        strokeJoin = Paint.Join.ROUND
    }

    private var normalizedX = 0f
    private var normalizedY = 0f
    private var cursorVisible = false

    init {
        // Plain Views with no background are optimized to skip onDraw()
        // entirely (the WILL_NOT_DRAW flag) unless told otherwise — since
        // this view is fully transparent with no background, onDraw()
        // would silently never run without this.
        setWillNotDraw(false)
    }

    fun updateCursor(x: Float, y: Float, visible: Boolean) {
        normalizedX = x.coerceIn(0f, 1f)
        normalizedY = y.coerceIn(0f, 1f)
        cursorVisible = visible
        postInvalidateOnAnimation()
    }

    override fun onDraw(canvas: Canvas) {
        super.onDraw(canvas)
        if (!cursorVisible) return
        val x = min(max(0f, normalizedX * width), max(0f, width - cursorWidth))
        val y = min(max(0f, normalizedY * height), max(0f, height - cursorHeight))
        canvas.save()
        canvas.translate(x, y)
        canvas.drawPath(cursorPath, fill)
        canvas.drawPath(cursorPath, outline)
        canvas.restore()
    }
}

