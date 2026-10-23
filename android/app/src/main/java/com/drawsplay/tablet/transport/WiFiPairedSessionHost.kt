package com.drawsplay.tablet.transport

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkAddress
import android.net.NetworkCapabilities
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.os.Build
import android.util.Log
import android.view.Surface
import com.drawsplay.tablet.video.WiFiVideoPlayer
import java.io.IOException
import java.net.Inet4Address
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.net.Socket
import java.net.SocketTimeoutException
import java.util.concurrent.atomic.AtomicBoolean
import javax.net.ssl.X509KeyManager

/**
 * Accepts one saved-trust Wi-Fi session at a time: advertises the tablet on the LAN, accepts the
 * host's control and video sockets, offers the control link to the input channel and plays video.
 * Inert without a saved paired host. USB always wins: [wifiAllowed] false tears the session down
 * and stops the advertisement so the host releases the display for USB.
 */
class WiFiPairedSessionHost(
    context: Context,
    private val selector: HostEndpointSelector,
    private val surface: () -> Surface?,
    private val wifiAllowed: () -> Boolean
) {
    private val app = context.applicationContext
    private val running = AtomicBoolean(false)
    @Volatile private var thread: Thread? = null
    @Volatile private var session: ActiveSession? = null

    private class ActiveSession(val closers: List<() -> Unit>) {
        fun close() = closers.forEach { it() }
    }

    fun start() {
        if (!running.compareAndSet(false, true)) return
        thread = Thread({ loop() }, "wifi-paired-host").also { it.isDaemon = true; it.start() }
    }

    fun stop() {
        if (!running.compareAndSet(true, false)) return
        session?.close()
        thread?.interrupt()
        thread = null
        selector.updatePairedControl(null)
    }

    private fun loop() {
        while (running.get()) {
            try {
                val store = WiFiTrustStore(app)
                val host = store.readPairedHost()
                if (host == null || !wifiAllowed()) {
                    Thread.sleep(IDLE_MS)
                    continue
                }
                runSession(store, host.spkiSha256)
            } catch (_: InterruptedException) {
                return
            } catch (error: Exception) {
                Log.w(TAG, "Wi-Fi session ended: ${error.message}")
                selector.updatePairedControl(null)
                try { Thread.sleep(RETRY_MS) } catch (_: InterruptedException) { return }
            }
        }
    }

    private fun runSession(store: WiFiTrustStore, hostPin: ByteArray) {
        val (local, network) = WiFiLan.select(app) ?: run { Thread.sleep(IDLE_MS); return }
        val control = ServerSocket().apply { bind(InetSocketAddress(local.address, 0), 1); soTimeout = ACCEPT_POLL_MS }
        val video = ServerSocket().apply { bind(InetSocketAddress(local.address, 0), 1); soTimeout = VIDEO_ACCEPT_MS }
        val nsd = app.getSystemService(Context.NSD_SERVICE) as NsdManager
        val registration = object : NsdManager.RegistrationListener {
            override fun onServiceRegistered(info: NsdServiceInfo) {}
            override fun onRegistrationFailed(info: NsdServiceInfo, error: Int) { Log.w(TAG, "NSD registration failed ($error)") }
            override fun onServiceUnregistered(info: NsdServiceInfo) {}
            override fun onUnregistrationFailed(info: NsdServiceInfo, error: Int) {}
        }
        var registered = false
        var controlSession: WiFiPairedSession.Control? = null
        val watcher = Thread {
            while (running.get() && !Thread.currentThread().isInterrupted) {
                if (!wifiAllowed()) { session?.close(); control.close(); video.close(); return@Thread }
                try { Thread.sleep(WATCH_MS) } catch (_: InterruptedException) { return@Thread }
            }
        }.apply { isDaemon = true; start() }
        try {
            nsd.registerService(NsdServiceInfo().apply {
                serviceName = "DrawSplay-${Build.MODEL}".take(48)
                serviceType = "_drawsplay-tablet._tcp."
                port = control.localPort
                if (Build.VERSION.SDK_INT >= 33) setNetwork(network)
                setAttribute("v", "2")
                setAttribute("videoPort", video.localPort.toString())
                setAttribute("pairing", "0")
                setAttribute("streaming", "1")
            }, NsdManager.PROTOCOL_DNS_SD, registration)
            registered = true
            val raw = acceptDirectPeer(control, local)
            val pairedSession = WiFiPairedSession(
                store.keyManagers().filterIsInstance<X509KeyManager>().single(), hostPin
            )
            val established = pairedSession.acceptControl(raw, CAPABILITIES, HANDSHAKE_MS)
            controlSession = established
            val media = pairedSession.acceptVideo(acceptDirectPeer(video, local), established, HANDSHAKE_MS)
            val link = WiFiControlLink(established.socket.inputStream, established.socket.outputStream, established.epoch)
            val active = ActiveSession(listOf(
                { try { established.close() } catch (_: IOException) {} },
                { try { media.close() } catch (_: IOException) {} }
            ))
            session = active
            val linkSocket = LinkSocket(link) { active.close() }
            // The control socket and the paired Wi-Fi endpoint stop being offered once the session ends.
            selector.updatePairedControl(linkSocket)
            val target = waitForSurface()
            WiFiVideoPlayer.run(media.inputStream, { active.close() }, link, established.limits, established.epoch, target) {
                !running.get() || !wifiAllowed()
            }
        } finally {
            watcher.interrupt()
            selector.updatePairedControl(null)
            session?.close()
            session = null
            try { controlSession?.close() } catch (_: IOException) {}
            if (registered) try { nsd.unregisterService(registration) } catch (_: IllegalArgumentException) {}
            try { control.close() } catch (_: IOException) {}
            try { video.close() } catch (_: IOException) {}
        }
    }

    private fun waitForSurface(): Surface {
        repeat(SURFACE_WAIT_POLLS) {
            surface()?.let { return it }
            Thread.sleep(100)
        }
        throw IOException("No video surface for Wi-Fi session")
    }

    private fun acceptDirectPeer(server: ServerSocket, local: LinkAddress): Socket {
        while (running.get()) {
            val raw = try { server.accept() } catch (_: SocketTimeoutException) {
                if (server.soTimeout == VIDEO_ACCEPT_MS) throw IOException("Host did not open the Wi-Fi video socket")
                continue
            }
            if (!WiFiLanPolicy.isDirectPeer(local.address, local.prefixLength, raw.inetAddress)) {
                raw.close()
                continue
            }
            raw.tcpNoDelay = true
            return raw
        }
        throw IOException("Wi-Fi session host stopped")
    }

    companion object {
        private const val TAG = "WiFiPairedHost"
        private const val IDLE_MS = 3000L
        private const val RETRY_MS = 2000L
        private const val WATCH_MS = 1000L
        private const val ACCEPT_POLL_MS = 1000
        private const val VIDEO_ACCEPT_MS = 10000
        private const val HANDSHAKE_MS = 5000
        private const val SURFACE_WAIT_POLLS = 50

        private val CAPABILITIES = WiFiSessionProtocol.Hello(
            minMajor = 2, maxMajor = 2, role = WiFiSessionProtocol.TABLET, codecMask = 1,
            maxWidth = 1920, maxHeight = 1200, maxFps = 60, h264Level = 42,
            maxAccessUnit = 512 * 1024, queueBytes = 1536 * 1024, queueSlots = 3,
            statsVersion = 1, features = 1, spki = ByteArray(32), nonce = ByteArray(32)
        )
    }
}
