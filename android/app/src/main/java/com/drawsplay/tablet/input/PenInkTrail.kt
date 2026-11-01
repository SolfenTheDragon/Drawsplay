package com.drawsplay.tablet.input

/**
 * Provisional pen ink for the instant ink preview.
 *
 * The tablet draws each stylus stroke as soon as the digitizer reports it,
 * then erases each part once a video frame that contains the host's own
 * rendering of it has been shown. A frame's metadata names the newest input
 * the host had injected when it captured the frame; input is sent in order,
 * so every point recorded before that input was sent is in the frame
 * ([eraseReflected]). [eraseOlderThan] is the fallback for pipelines that
 * send no frame metadata, and a safety net if frames stop arriving.
 *
 * The newest erased point stays as the stroke's anchor, so the remaining
 * preview joins the host's ink without a gap. Pure Kotlin: drawing is done
 * by [InputPadView]; all times are System.nanoTime() values.
 */
class PenInkTrail {
    data class Point(val x: Float, val y: Float, val pressure: Float, val timeNs: Long)

    class Stroke {
        val points = ArrayList<Point>()
        /** Newest point already shown by the host; preview starts here. */
        var anchor: Point? = null
        var open = true
    }

    val strokes = ArrayDeque<Stroke>()

    val isEmpty: Boolean get() = strokes.isEmpty()

    fun begin(p: Point) {
        strokes.lastOrNull()?.open = false
        strokes.addLast(Stroke().also { it.points.add(p) })
        while (strokes.size > MAX_STROKES) strokes.removeFirst()
    }

    fun add(p: Point) {
        val s = strokes.lastOrNull()
        if (s == null || !s.open) {
            begin(p)
            return
        }
        s.points.add(p)
        if (s.points.size > MAX_POINTS) {
            s.anchor = s.points.removeAt(0)
        }
    }

    fun end() {
        strokes.lastOrNull()?.open = false
    }

    fun clear() = strokes.clear()

    /**
     * The host has shown everything sent up to [sentNs]: erase points
     * recorded before that, minus [graceNs] for the host app's own drawing
     * lag. Returns true when anything changed.
     */
    fun eraseReflected(sentNs: Long, graceNs: Long = REFLECT_GRACE_NS): Boolean =
        eraseThrough(sentNs - graceNs)

    /** Erases points recorded more than [maxAgeNs] before [nowNs]. */
    fun eraseOlderThan(nowNs: Long, maxAgeNs: Long): Boolean = eraseThrough(nowNs - maxAgeNs)

    private fun eraseThrough(cutoffNs: Long): Boolean {
        var changed = false
        val it = strokes.iterator()
        while (it.hasNext()) {
            val s = it.next()
            var n = 0
            while (n < s.points.size && s.points[n].timeNs <= cutoffNs) n++
            if (n == 0) continue
            changed = true
            s.anchor = s.points[n - 1]
            if (n == s.points.size && !s.open) {
                it.remove()
            } else {
                s.points.subList(0, n).clear()
            }
        }
        return changed
    }

    companion object {
        const val MAX_STROKES = 32
        const val MAX_POINTS = 2048
        /**
         * A frame's metadata names the newest input injected before capture,
         * but the host app paints that input one or two of its own frames
         * later; this margin keeps the preview until the app has drawn it.
         */
        const val REFLECT_GRACE_NS = 35_000_000L
        /** Fallback lifetime without frame metadata (ffmpeg pipelines). */
        const val UNREFLECTED_MAX_AGE_NS = 150_000_000L
        /** Safety lifetime even with frame metadata. */
        const val MAX_AGE_NS = 600_000_000L
    }
}
