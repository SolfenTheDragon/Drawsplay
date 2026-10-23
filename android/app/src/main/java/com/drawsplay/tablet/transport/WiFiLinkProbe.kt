package com.drawsplay.tablet.transport

import android.content.Context
import android.net.wifi.WifiManager
import com.drawsplay.tablet.video.WiFiVideoStats

/** Samples the tablet's current Wi-Fi association for the host's live signal indicator. */
class WiFiLinkProbe(context: Context) {
    private val wifi = context.applicationContext.getSystemService(Context.WIFI_SERVICE) as? WifiManager

    @Suppress("DEPRECATION") // connectionInfo still reports RSSI/link speed without location permission.
    fun sample(): WiFiVideoStats.Link? {
        val info = wifi?.connectionInfo ?: return null
        if (info.networkId == -1 || info.rssi !in -126..-1) return null
        return WiFiVideoStats.Link(info.rssi, info.linkSpeed.coerceAtLeast(0), info.frequency.coerceAtLeast(0))
    }
}
