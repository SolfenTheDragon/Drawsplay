package com.drawsplay.tablet.transport

data class HostEndpoint(
    val host: String,
    val inputPort: Int,
    val videoPort: Int,
    val authToken: String = "",
    val tls: Boolean = false,
    val tlsFingerprintSha256: String = ""
)

class HostEndpointSelector {
    @Volatile
    private var wifiEndpoint: HostEndpoint? = null

    fun updateWifiEndpoint(endpoint: HostEndpoint?) {
        wifiEndpoint = endpoint
    }

    fun inputCandidates(): List<HostEndpoint> {
        val usb = HostEndpoint(host = "127.0.0.1", inputPort = 27183, videoPort = 27184)
        val wifi = wifiEndpoint
        return if (wifi != null) listOf(usb, wifi) else listOf(usb)
    }

    fun videoCandidates(): List<HostEndpoint> = inputCandidates()
}

