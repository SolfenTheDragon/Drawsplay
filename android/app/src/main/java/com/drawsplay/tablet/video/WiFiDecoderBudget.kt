package com.drawsplay.tablet.video

import android.media.MediaCodecInfo
import android.media.MediaCodecList
import android.media.MediaFormat

/**
 * Frame-rate ceiling advertised in the Wi-Fi session Hello. A stream above the
 * hardware decoder's rated throughput leaves it no slack to drain network
 * bursts, so the queue overflows and forces keyframe restarts.
 */
object WiFiDecoderBudget {
    const val CEILING_FPS = 60
    const val FLOOR_FPS = 24

    fun maxFps(ratedFps: Double?): Int {
        if (ratedFps == null || ratedFps.isNaN() || ratedFps <= 0.0) return CEILING_FPS
        return ratedFps.toInt().coerceIn(FLOOR_FPS, CEILING_FPS)
    }

    /** Rated fps at [width]x[height] for the decoder MediaCodec.createDecoderByType picks. */
    fun forDevice(width: Int, height: Int): Int = maxFps(runCatching {
        val name = MediaCodecList(MediaCodecList.REGULAR_CODECS)
            .findDecoderForFormat(MediaFormat.createVideoFormat(MediaFormat.MIMETYPE_VIDEO_AVC, width, height))
        MediaCodecList(MediaCodecList.REGULAR_CODECS).codecInfos
            .first { it.name == name }
            .getCapabilitiesForType(MediaFormat.MIMETYPE_VIDEO_AVC)
            .videoCapabilities.ratedFps(width, height)
    }.getOrNull())

    private fun MediaCodecInfo.VideoCapabilities.ratedFps(width: Int, height: Int): Double =
        runCatching { getSupportedFrameRatesFor(width, height).upper }
            .getOrElse { getSupportedFrameRatesFor(height, width).upper }
}
