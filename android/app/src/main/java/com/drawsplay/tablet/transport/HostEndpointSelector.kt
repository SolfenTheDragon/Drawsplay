package com.drawsplay.tablet.transport

data class HostEndpoint(
    val host: String,
    val inputPort: Int,
    val videoPort: Int,
    val authToken: String = "",
    val tls: Boolean = false,
    val tlsFingerprintSha256: String = "",
    val accessory: Boolean = false
)

class HostEndpointSelector {
    @Volatile
    private var wifiEndpoint: HostEndpoint? = null

    @Volatile
    private var accessoryAttached = false

    fun updateAccessoryAttached(attached: Boolean) {
        accessoryAttached = attached
    }

    fun updateWifiEndpoint(endpoint: HostEndpoint?) {
        wifiEndpoint = endpoint
    }

    fun inputCandidates(): List<HostEndpoint> {
        val usb = HostEndpoint(host = "127.0.0.1", inputPort = 27183, videoPort = 27184)
        val wifi = wifiEndpoint
        val candidates = mutableListOf<HostEndpoint>()
        if (accessoryAttached) {
            candidates += HostEndpoint(host = "usb-accessory", inputPort = 0, videoPort = 0, accessory = true)
        }
        candidates += usb
        if (wifi != null) candidates += wifi
        return candidates
    }

    fun videoCandidates(): List<HostEndpoint> = inputCandidates()
}

