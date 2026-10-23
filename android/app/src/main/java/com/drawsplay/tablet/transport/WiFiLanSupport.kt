package com.drawsplay.tablet.transport

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkAddress
import android.net.Network
import android.net.NetworkCapabilities
import java.io.InputStream
import java.io.OutputStream
import java.net.Inet4Address
import java.net.InetAddress
import java.net.Socket

/** USB (accessory, or a USB-powered tablet with adb on) always wins over Wi-Fi. */
object UsbPriorityPolicy {
    fun allowWifi(accessoryAttached: Boolean, usbPowered: Boolean, adbEnabled: Boolean): Boolean =
        !accessoryAttached && !(usbPowered && adbEnabled)
}

object WiFiLanPolicy {
    /** True only for another host on the same directly connected IPv4 subnet. */
    fun isDirectPeer(local: InetAddress, prefixLength: Int, peer: InetAddress): Boolean {
        if (local !is Inet4Address || peer !is Inet4Address || prefixLength !in 1..30) return false
        if (peer == local) return false
        fun number(address: InetAddress) = address.address.fold(0) { value, byte -> (value shl 8) or (byte.toInt() and 255) }
        val mask = -1 shl (32 - prefixLength)
        val subnet = number(local) and mask
        val candidate = number(peer)
        return (candidate and mask) == subnet && candidate != subnet && candidate != (subnet or mask.inv())
    }
}

/** A Socket whose streams are the paired control link; [onClose] tears down the real sockets once. */
class LinkSocket(private val link: WiFiControlLink, private val onClose: () -> Unit) : Socket() {
    @Volatile private var closed = false
    override fun getInputStream(): InputStream = link.inputStream
    override fun getOutputStream(): OutputStream = link.outputStream
    override fun isClosed(): Boolean = closed
    override fun isConnected(): Boolean = !closed
    override fun setTcpNoDelay(on: Boolean) {}
    override fun setKeepAlive(on: Boolean) {}
    @Synchronized override fun close() {
        if (closed) return
        closed = true
        onClose()
    }
}

object WiFiLan {
    /** Exactly one non-VPN Wi-Fi network with exactly one IPv4 address, else null. */
    fun select(context: Context): Pair<LinkAddress, Network>? {
        val manager = context.applicationContext.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        val network = manager.allNetworks.filter { candidate ->
            manager.getNetworkCapabilities(candidate)?.let {
                it.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) && !it.hasTransport(NetworkCapabilities.TRANSPORT_VPN)
            } == true
        }.singleOrNull() ?: return null
        val addresses = manager.getLinkProperties(network)?.linkAddresses
            ?.filter { it.address is Inet4Address && !it.address.isLoopbackAddress } ?: return null
        return addresses.singleOrNull()?.let { it to network }
    }
}
