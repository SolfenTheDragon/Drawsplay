package com.drawsplay.tablet.transport

data class HostEndpoint(
    val host: String,
    val inputPort: Int,
    val videoPort: Int,
    val accessory: Boolean = false,
    /** An already-authenticated inbound paired Wi-Fi control socket; only for input. */
    val pairedSocket: java.net.Socket? = null
)

/**
 * Orders authenticated ways the app can reach the host: USB accessory, then
 * a paired Wi-Fi session.
 *
 * The legacy ADB reverse endpoint is plain, unauthenticated TCP. A local app
 * could pose as the host and receive pen input and keystrokes, so this client
 * deliberately does not use it. [adbEnabled] remains in the constructor for
 * source compatibility with existing call sites.
 */
class HostEndpointSelector(@Suppress("unused") private val adbEnabled: () -> Boolean = { false }) {
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
        val candidates = mutableListOf<HostEndpoint>()
        if (accessoryAttached) {
            candidates += HostEndpoint(host = "usb-accessory", inputPort = 0, videoPort = 0, accessory = true)
        }
        return candidates
    }

    fun videoCandidates(): List<HostEndpoint> = streamCandidates()

    companion object {
        const val ADB_INPUT_PORT = 27183
        const val ADB_VIDEO_PORT = 27184
    }
}
