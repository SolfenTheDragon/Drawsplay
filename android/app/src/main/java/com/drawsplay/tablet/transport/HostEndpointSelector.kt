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
 * Orders the ways the app can reach the host: USB accessory, then the adb
 * reverse tunnel on this device's loopback, then a paired Wi-Fi session.
 *
 * The loopback endpoint is plain, unauthenticated TCP. Without adb reverse
 * nothing legitimate listens there, and any other app on the tablet could
 * open 127.0.0.1:27183/27184 and pose as the host (receiving pen input and
 * keystrokes). It is therefore offered only while USB debugging is on, which
 * the adb tunnel needs anyway; [adbEnabled] reports that setting.
 */
class HostEndpointSelector(private val adbEnabled: () -> Boolean = { false }) {
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
        if (adbEnabled()) {
            candidates += HostEndpoint(host = "127.0.0.1", inputPort = ADB_INPUT_PORT, videoPort = ADB_VIDEO_PORT)
        }
        return candidates
    }

    fun videoCandidates(): List<HostEndpoint> = streamCandidates()

    companion object {
        const val ADB_INPUT_PORT = 27183
        const val ADB_VIDEO_PORT = 27184
    }
}
