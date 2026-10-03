package com.drawsplay.tablet

import android.os.Build

/**
 * Runtime Android capability profile.
 *
 * The app supports Android 8+ (API 26) and selects newer platform behavior
 * only when those APIs exist on the current device.
 */
object AndroidCompat {
    val sdkInt: Int = Build.VERSION.SDK_INT

    val supportsWindowMetrics: Boolean = sdkInt >= Build.VERSION_CODES.R
    val supportsSurfaceFrameRateHint: Boolean = sdkInt >= Build.VERSION_CODES.R
    val supportsSurfaceFrameRateAlways: Boolean = sdkInt >= Build.VERSION_CODES.S
    val supportsLowLatencyDecoderHint: Boolean = sdkInt >= Build.VERSION_CODES.R
    val supportsPackageInfoFlagsApi: Boolean = sdkInt >= Build.VERSION_CODES.TIRAMISU
}
