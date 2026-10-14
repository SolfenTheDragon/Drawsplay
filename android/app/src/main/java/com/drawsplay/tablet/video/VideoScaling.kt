package com.drawsplay.tablet.video

import kotlin.math.min
import kotlin.math.roundToInt

/**
 * How the host picture is laid out on the tablet panel, as chosen in the
 * host's settings (sent in the session policy):
 *  - [FILL]: stretched over the whole panel (aspect ratio not kept).
 *  - [FIT]: as large as possible with the aspect ratio kept (black bars).
 *  - [ORIGINAL]: one host pixel per panel pixel, centred; a picture larger
 *    than the panel is scaled down like [FIT] so no part of it is unreachable.
 */
enum class VideoScaling(val wireName: String) {
    FILL("fill"),
    FIT("fit"),
    ORIGINAL("original");

    /**
     * Size in pixels of the video frame (surface, input pad and cursor) for a
     * [videoWidth]x[videoHeight] picture on an [availWidth]x[availHeight]
     * panel, or null to fill the panel (also used while the size is unknown).
     */
    fun frameSize(videoWidth: Int, videoHeight: Int, availWidth: Int, availHeight: Int): Pair<Int, Int>? {
        if (this == FILL || videoWidth <= 0 || videoHeight <= 0 || availWidth <= 0 || availHeight <= 0) {
            return null
        }
        if (this == ORIGINAL && videoWidth <= availWidth && videoHeight <= availHeight) {
            return videoWidth to videoHeight
        }
        val scale = min(availWidth.toDouble() / videoWidth, availHeight.toDouble() / videoHeight)
        return min(availWidth, (videoWidth * scale).roundToInt()) to
            min(availHeight, (videoHeight * scale).roundToInt())
    }

    companion object {
        val DEFAULT = FILL

        fun fromWireName(name: String?): VideoScaling? =
            values().firstOrNull { it.wireName == name?.trim()?.lowercase() }
    }
}
