package com.drawsplay.tablet

import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.content.ContextCompat

/**
 * Android 17 local network protection.
 *
 * Apps that target API 37 need the ACCESS_LOCAL_NETWORK runtime permission
 * (Nearby devices group) for any LAN traffic: mDNS registration, the tablet's
 * listening sockets that the computer connects to, and TCP to LAN addresses.
 * Without it, accepts and connects simply time out, which would look like a
 * missing computer. DrawSplay's Wi-Fi pairing and reconnect paths therefore
 * check this before they start, and ask for it instead of waiting.
 *
 * USB (Android Open Accessory) is not a network, so it does not need the
 * permission.
 */
object LocalNetworkAccess {
    const val PERMISSION = "android.permission.ACCESS_LOCAL_NETWORK"

    /** Android 17; also the target SDK from which the permission is enforced. */
    const val ENFORCED_FROM_SDK = 37

    /** True when this device enforces the permission for an app with this target SDK. */
    fun enforced(deviceSdk: Int, targetSdk: Int): Boolean =
        deviceSdk >= ENFORCED_FROM_SDK && targetSdk >= ENFORCED_FROM_SDK

    fun required(context: Context): Boolean =
        enforced(Build.VERSION.SDK_INT, context.applicationInfo.targetSdkVersion)

    /** Whether Wi-Fi may use the local network now (always true before Android 17). */
    fun granted(context: Context): Boolean =
        !required(context) ||
            ContextCompat.checkSelfPermission(context, PERMISSION) == PackageManager.PERMISSION_GRANTED
}
