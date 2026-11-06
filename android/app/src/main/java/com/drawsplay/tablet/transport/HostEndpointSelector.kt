package com.drawsplay.tablet.transport

data class HostEndpoint(
    val host: String,
    val inputPort: Int,
    val videoPort: Int,
    val accessory: Boolean = false,
    /** An already-authenticated inbound paired Wi-Fi control socket; only for input. */
    val pairedSocket: java.net.Socket? = null
)

class HostEndpointSelector {
    @Volatile
    private var accessoryAttached = false

    fun isAccessoryAttached(): Boolean = accessoryAttached

    fun updateAccessoryAttached(attached: Boolean) {
        accessoryAttached = attached
    }

    @Volatile
    private var pairedControl: java.net.Socket? = null

    fun updatePairedControl(socket: java.net.Socket?) {
        pairedControl = socket
    }

    fun inputCandidates(): List<HostEndpoint> {
        val candidates = streamCandidates()
        val paired = pairedControl
        // Paired Wi-Fi stays last so USB and accessory keep their priority.
        return if (paired != null && !paired.isClosed) {
            candidates + HostEndpoint(host = "wifi-paired", inputPort = 0, videoPort = 0, pairedSocket = paired)
        } else candidates
    }

    private fun streamCandidates(): List<HostEndpoint> {
        val usb = HostEndpoint(host = "127.0.0.1", inputPort = 27183, videoPort = 27184)
        val candidates = mutableListOf<HostEndpoint>()
        if (accessoryAttached) {
            candidates += HostEndpoint(host = "usb-accessory", inputPort = 0, videoPort = 0, accessory = true)
        }
        candidates += usb
        return candidates
    }

    fun videoCandidates(): List<HostEndpoint> = streamCandidates()
}

