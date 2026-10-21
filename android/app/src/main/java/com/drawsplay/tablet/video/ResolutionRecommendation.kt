package com.drawsplay.tablet.video

import android.app.ActivityManager
import android.content.Context
import android.media.MediaCodecInfo
import android.media.MediaCodecList
import kotlin.math.sqrt

/** A suggested ceiling for the host display resolution, shown in the host UI. */
data class ResolutionRecommendation(val width: Int, val height: Int, val reason: String)

/**
 * Suggests a maximum streaming resolution from the tablet's specs so low-spec
 * devices are not pushed past what they can decode and present smoothly.
 * Returns null when the native resolution is fine.
 */
object ResolutionRecommender {
    private const val MB = 1024L * 1024L

    // Pixel budgets per RAM tier. 1200x800 (0.96 MP) is known good on 2-3 GB
    // tablets; 1920x1200 (2.3 MP) stutters there.
    private const val LOW_RAM_MAX_MB = 3584L
    private const val LOW_RAM_PIXELS = 1_100_000
    private const val MID_RAM_MAX_MB = 4608L
    private const val MID_RAM_PIXELS = 2_100_000

    fun recommend(
        nativeWidth: Int,
        nativeHeight: Int,
        totalRamMb: Long,
        lowRamDevice: Boolean,
        decoderSupports60fps: (Int, Int) -> Boolean
    ): ResolutionRecommendation? {
        if (nativeWidth <= 0 || nativeHeight <= 0) return null
        val native = nativeWidth.toLong() * nativeHeight
        var budget = Long.MAX_VALUE
        var reason = ""
        when {
            lowRamDevice || (totalRamMb in 1..LOW_RAM_MAX_MB) -> {
                budget = LOW_RAM_PIXELS.toLong()
                reason = "limited memory (${formatGb(totalRamMb)} GB RAM)"
            }
            totalRamMb in 1..MID_RAM_MAX_MB -> {
                budget = MID_RAM_PIXELS.toLong()
                reason = "moderate memory (${formatGb(totalRamMb)} GB RAM)"
            }
        }
        if (native <= budget && decoderSupports60fps(nativeWidth, nativeHeight)) return null

        var w = nativeWidth
        var h = nativeHeight
        if (native > budget) {
            val scale = sqrt(budget.toDouble() / native)
            w = alignDown((nativeWidth * scale).toInt())
            h = alignDown((nativeHeight * scale).toInt())
        }
        // Step down further while the hardware decoder cannot do 60 fps at this size.
        var guard = 0
        while (w > 640 && !decoderSupports60fps(w, h) && guard++ < 12) {
            w = alignDown((w * 0.85).toInt())
            h = alignDown((h * 0.85).toInt())
            reason = "hardware decoder limit"
        }
        val (sw, sh) = snapToStandard(w, h)
        if (sw >= nativeWidth && sh >= nativeHeight) return null
        return ResolutionRecommendation(sw, sh, reason)
    }

    /** Reads this device's RAM and decoder limits and recommends a resolution. */
    fun forDevice(context: Context, nativeWidth: Int, nativeHeight: Int): ResolutionRecommendation? =
        runCatching {
            val am = context.getSystemService(Context.ACTIVITY_SERVICE) as ActivityManager
            val info = ActivityManager.MemoryInfo().also { am.getMemoryInfo(it) }
            val caps = hardwareAvcCapabilities()
            recommend(
                nativeWidth,
                nativeHeight,
                info.totalMem / MB,
                am.isLowRamDevice
            ) { w, h -> caps == null || runCatching { caps.areSizeAndRateSupported(w, h, 60.0) }.getOrDefault(true) }
        }.getOrNull()

    private fun hardwareAvcCapabilities(): MediaCodecInfo.VideoCapabilities? =
        MediaCodecList(MediaCodecList.REGULAR_CODECS).codecInfos
            .filter { !it.isEncoder && "video/avc" in it.supportedTypes }
            .filter { !it.name.startsWith("c2.android.") && !it.name.startsWith("OMX.google.") }
            .firstNotNullOfOrNull {
                runCatching { it.getCapabilitiesForType("video/avc").videoCapabilities }.getOrNull()
            }

    // Landscape sizes the recommendation is rounded down to.
    internal val STANDARD_RESOLUTIONS = listOf(
        800 to 480, 1024 to 600, 1024 to 768, 1200 to 800, 1280 to 720, 1280 to 800, 1366 to 768,
        1440 to 900, 1600 to 900, 1600 to 1200, 1920 to 1080, 1920 to 1200, 2000 to 1200,
        2560 to 1440, 2560 to 1600
    )

    /** Largest standard resolution that fits within [w]x[h]; the input itself if none does. */
    internal fun snapToStandard(w: Int, h: Int): Pair<Int, Int> =
        STANDARD_RESOLUTIONS.filter { it.first <= w && it.second <= h }
            .maxByOrNull { it.first.toLong() * it.second } ?: (w to h)

    private fun alignDown(v: Int): Int = (v / 8) * 8

    private fun formatGb(mb: Long): String = String.format(java.util.Locale.US, "%.1f", mb / 1024.0)
}
