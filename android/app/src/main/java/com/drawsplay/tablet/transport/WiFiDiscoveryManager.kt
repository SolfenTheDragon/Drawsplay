package com.drawsplay.tablet.transport

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.net.wifi.WifiManager
import android.util.Log
import java.util.concurrent.atomic.AtomicBoolean

class WiFiDiscoveryManager(
    context: Context,
    private val selector: HostEndpointSelector,
    private val authTokenProvider: () -> String,
    private val tlsRequiredProvider: () -> Boolean,
    private val tlsFingerprintProvider: () -> String
) {
    private val appContext = context.applicationContext
    private val nsdManager = appContext.getSystemService(Context.NSD_SERVICE) as NsdManager
    private val wifiManager = appContext.getSystemService(Context.WIFI_SERVICE) as WifiManager
    private var multicastLock: WifiManager.MulticastLock? = null
    private val started = AtomicBoolean(false)

    private val discoveryListener = object : NsdManager.DiscoveryListener {
        override fun onStartDiscoveryFailed(serviceType: String?, errorCode: Int) {
            Log.w(TAG, "NSD start failed: $errorCode")
            stop()
        }

        override fun onStopDiscoveryFailed(serviceType: String?, errorCode: Int) {
            Log.w(TAG, "NSD stop failed: $errorCode")
            try {
                nsdManager.stopServiceDiscovery(this)
            } catch (_: Exception) {
            }
        }

        override fun onDiscoveryStarted(serviceType: String?) = Unit

        override fun onDiscoveryStopped(serviceType: String?) = Unit

        override fun onServiceFound(serviceInfo: NsdServiceInfo) {
            if (serviceInfo.serviceType != SERVICE_TYPE) return
            nsdManager.resolveService(serviceInfo, object : NsdManager.ResolveListener {
                override fun onResolveFailed(serviceInfo: NsdServiceInfo?, errorCode: Int) {
                    Log.d(TAG, "Resolve failed: $errorCode")
                }

                override fun onServiceResolved(resolved: NsdServiceInfo) {
                    val host = resolved.host?.hostAddress ?: return
                    val inputPort = resolved.port
                    if (inputPort <= 0) return
                    val attrs = resolved.attributes
                    val videoPort = attrs["videoPort"]?.toString(Charsets.UTF_8)?.toIntOrNull() ?: 27184
                    val tlsAdvertised = attrs["tls"]?.toString(Charsets.UTF_8) == "1"
                    val fpAdvertised = attrs["tlsFp"]?.toString(Charsets.UTF_8) ?: ""
                    val authRequired = attrs["auth"]?.toString(Charsets.UTF_8) == "1"
                    val authToken = authTokenProvider().trim()
                    if (authRequired && authToken.isEmpty()) return
                    val tlsRequired = tlsRequiredProvider()
                    val tlsFingerprint = tlsFingerprintProvider().trim()
                    if (tlsRequired) {
                        if (!tlsAdvertised) return
                        if (tlsFingerprint.isEmpty()) return
                        if (!fpAdvertised.equals(tlsFingerprint, ignoreCase = true)) return
                    }
                    selector.updateWifiEndpoint(
                        HostEndpoint(
                            host = host,
                            inputPort = inputPort,
                            videoPort = videoPort,
                            authToken = authToken,
                            tls = tlsRequired,
                            tlsFingerprintSha256 = tlsFingerprint
                        )
                    )
                }
            })
        }

        override fun onServiceLost(serviceInfo: NsdServiceInfo) {
            selector.updateWifiEndpoint(null)
        }
    }

    fun start() {
        if (!started.compareAndSet(false, true)) return
        multicastLock = wifiManager.createMulticastLock("drawsplay-nsd").apply {
            setReferenceCounted(false)
            acquire()
        }
        nsdManager.discoverServices(SERVICE_TYPE, NsdManager.PROTOCOL_DNS_SD, discoveryListener)
    }

    fun stop() {
        if (!started.compareAndSet(true, false)) return
        try {
            nsdManager.stopServiceDiscovery(discoveryListener)
        } catch (_: Exception) {
        }
        selector.updateWifiEndpoint(null)
        multicastLock?.let {
            try {
                if (it.isHeld) it.release()
            } catch (_: Exception) {
            }
        }
        multicastLock = null
    }

    companion object {
        private const val TAG = "WiFiDiscoveryManager"
        private const val SERVICE_TYPE = "_drawsplay._tcp."
    }
}

