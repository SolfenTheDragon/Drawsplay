package com.drawsplay.tablet.input

import kotlin.math.hypot
import kotlin.math.max
import kotlin.math.min

/**
 * Extrapolates a pointer position a few milliseconds ahead from its recent
 * samples, to hide part of the capture → host → display round trip for the
 * host cursor.
 *
 * The estimate is a damped constant-velocity projection: velocity comes from
 * the most recent samples, the look-ahead is capped, and the projection is
 * suppressed when motion is slow or has just reversed, so the cursor never
 * overshoots a stop. Pure Kotlin so it is unit-testable off-device.
 */
class PenPredictor(
    private val lookaheadMs: Float = DEFAULT_LOOKAHEAD_MS,
    private val maxDistancePx: Float = MAX_DISTANCE_PX
) {
    data class Point(val x: Float, val y: Float)

    private val xs = FloatArray(HISTORY)
    private val ys = FloatArray(HISTORY)
    private val ts = LongArray(HISTORY)
    private var count = 0

    fun reset() {
        count = 0
    }

    /** Records a real sample; [timeMs] must be monotonic per gesture. */
    fun record(x: Float, y: Float, timeMs: Long) {
        if (count > 0 && timeMs <= ts[(count - 1) % HISTORY]) return
        val slot = count % HISTORY
        xs[slot] = x
        ys[slot] = y
        ts[slot] = timeMs
        count++
    }

    /** Predicted position, or null when there is too little or too slow motion to extrapolate. */
    fun predict(): Point? {
        if (count < MIN_SAMPLES) return null
        val newest = (count - 1) % HISTORY
        val prev = (count - 2) % HISTORY
        val dt = ts[newest] - ts[prev]
        if (dt <= 0L || dt > MAX_SAMPLE_GAP_MS) return null

        val vx = (xs[newest] - xs[prev]) / dt
        val vy = (ys[newest] - ys[prev]) / dt

        // A reversal against the previous segment means the pen is turning or
        // stopping; predicting along the new velocity would overshoot.
        if (count >= 3) {
            val older = (count - 3) % HISTORY
            val odt = ts[prev] - ts[older]
            if (odt > 0L) {
                val ox = (xs[prev] - xs[older]) / odt
                val oy = (ys[prev] - ys[older]) / odt
                if (vx * ox + vy * oy < 0f) return null
            }
        }

        val speed = hypot(vx, vy)
        if (speed < MIN_SPEED_PX_PER_MS) return null

        val horizon = min(lookaheadMs, dt * MAX_HORIZON_SAMPLES)
        var dx = vx * horizon
        var dy = vy * horizon
        val dist = hypot(dx, dy)
        if (dist > maxDistancePx) {
            val scale = maxDistancePx / dist
            dx *= scale
            dy *= scale
        }
        return Point(xs[newest] + dx, ys[newest] + dy)
    }

    companion object {
        const val DEFAULT_LOOKAHEAD_MS = 12f
        const val MAX_DISTANCE_PX = 48f
        private const val HISTORY = 4
        private const val MIN_SAMPLES = 2
        private const val MAX_SAMPLE_GAP_MS = 50L
        private const val MIN_SPEED_PX_PER_MS = 0.05f
        private const val MAX_HORIZON_SAMPLES = 3f

        internal fun clampToView(p: Point, width: Int, height: Int): Point =
            Point(
                max(0f, min(p.x, (width - 1).coerceAtLeast(0).toFloat())),
                max(0f, min(p.y, (height - 1).coerceAtLeast(0).toFloat()))
            )
    }
}
