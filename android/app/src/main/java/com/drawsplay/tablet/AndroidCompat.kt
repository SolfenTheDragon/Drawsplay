package com.drawsplay.tablet

import android.content.pm.PackageInfo
import android.content.pm.PackageManager
import android.os.Build

/**
 * Runtime Android capability profile.
 *
 * The app supports Android 8+ (API 26) and selects newer platform behavior
 * only when those APIs exist on the current device.
 *
 * The flags are the profile the app logs and reports to the host. Code that
 * calls a newer API guards it with `Build.VERSION.SDK_INT >= Build.VERSION_CODES.X`
 * written out, never a bare number or one of these flags, because lint only
 * recognises the written-out check.
 */
object AndroidCompat {
    val sdkInt: Int = Build.VERSION.SDK_INT

    val supportsWindowMetrics: Boolean = sdkInt >= Build.VERSION_CODES.R
    val supportsSurfaceFrameRateHint: Boolean = sdkInt >= Build.VERSION_CODES.R
    val supportsLowLatencyDecoderHint: Boolean = sdkInt >= Build.VERSION_CODES.R

    /** [PackageManager.getPackageInfo] through whichever overload this Android has. */
    fun packageInfo(pm: PackageManager, packageName: String, flags: Int = 0): PackageInfo =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            pm.getPackageInfo(packageName, PackageManager.PackageInfoFlags.of(flags.toLong()))
        } else {
            @Suppress("DEPRECATION")
            pm.getPackageInfo(packageName, flags)
        }

    /** [PackageManager.getPackageArchiveInfo] through whichever overload this Android has. */
    fun packageArchiveInfo(pm: PackageManager, path: String, flags: Int = 0): PackageInfo? =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            pm.getPackageArchiveInfo(path, PackageManager.PackageInfoFlags.of(flags.toLong()))
        } else {
            @Suppress("DEPRECATION")
            pm.getPackageArchiveInfo(path, flags)
        }
}
