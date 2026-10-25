package com.drawsplay.tablet.transport

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkAddress
import android.net.NetworkCapabilities
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.view.Surface
import com.drawsplay.tablet.video.WiFiVideoPlayer
import com.drawsplay.tablet.PairedConnectionStatus
import java.io.IOException
import java.net.Inet4Address
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.net.Socket
import java.net.SocketTimeoutException
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
    private val onStreaming: (Boolean) -> Unit = {},
    private val onStatus: (PairedConnectionStatus) -> Unit = {},
    private val onPictureReady: () -> Unit = {},
    private val wifiAllowed: () -> Boolean
) {
    private val app = context.applicationContext
    private val main = Handler(Looper.getMainLooper())
    private val linkProbe = WiFiLinkProbe(app)
    @Volatile private var thread: Thread? = null
    @Volatile private var currentRun: WiFiSessionCancellation? = null

    @Synchronized
    fun start() {
        if (currentRun != null) return
        val run = WiFiSessionCancellation()
        currentRun = run
        thread = Thread({ loop(run) }, "wifi-paired-host").also { it.isDaemon = true; it.start() }
    }

    @Synchronized
    fun stop() {
        val run = currentRun ?: return
        currentRun = null
        try {
            run.close()
        } catch (error: IOException) {
            Log.w(TAG, "Cannot close all Wi-Fi session resources", error)
        }
        thread?.interrupt()
        thread = null
        selector.updatePairedControl(null)
        onStreaming(false)
    }

    private fun loop(run: WiFiSessionCancellation) {
        while (run.isActive) {
            try {
                val store = WiFiTrustStore(app)
                val host = store.readPairedHost()
                if (host == null || !wifiAllowed()) {
                    post(run) { onStatus(PairedConnectionStatus.IDLE) }
                    Thread.sleep(IDLE_MS)
                    continue
                }
                runSession(run, store, host.spkiSha256)
            } catch (_: InterruptedException) {
                return
            } catch (error: Exception) {
                if (!run.isActive) return
                Log.w(TAG, "Wi-Fi session ended: ${error.message}")
                post(run) {
                    onStatus(when (error) {
                        is WiFiTrustException -> if (error.failure == WiFiTrustFailure.IDENTITY) PairedConnectionStatus.IDENTITY_REPAIR else PairedConnectionStatus.TRUST_REPAIR
                        else -> PairedConnectionStatus.RECONNECTING
                    })
                }
                try { Thread.sleep(RETRY_MS) } catch (_: InterruptedException) { return }
            }
        }
    }

    private fun runSession(run: WiFiSessionCancellation, store: WiFiTrustStore, hostPin: ByteArray) {
        val (local, network) = WiFiLan.select(app) ?: run {
            post(run) { onStatus(PairedConnectionStatus.NETWORK_UNAVAILABLE) }
            Thread.sleep(IDLE_MS)
            return
        }
        post(run) { onStatus(PairedConnectionStatus.RECONNECTING) }
        val keyManager = store.keyManagers().filterIsInstance<X509KeyManager>().single()
        val resources = run.track(WiFiSessionCancellation())
        val nsd = app.getSystemService(Context.NSD_SERVICE) as NsdManager
        val registration = object : NsdManager.RegistrationListener {
            override fun onServiceRegistered(info: NsdServiceInfo) {}
            override fun onRegistrationFailed(info: NsdServiceInfo, error: Int) { Log.w(TAG, "NSD registration failed ($error)") }
            override fun onServiceUnregistered(info: NsdServiceInfo) {}
            override fun onUnregistrationFailed(info: NsdServiceInfo, error: Int) {}
        }
        var registered = false
        val watcher = Thread {
            while (run.isActive && resources.isActive && !Thread.currentThread().isInterrupted) {
                if (!wifiAllowed()) {
                    try { resources.close() } catch (error: IOException) { Log.w(TAG, "Close disallowed Wi-Fi session", error) }
                    return@Thread
                }
                try { Thread.sleep(WATCH_MS) } catch (_: InterruptedException) { return@Thread }
            }
        }.apply { isDaemon = true; start() }
        try {
            val control = resources.track(ServerSocket()).apply {
                bind(InetSocketAddress(local.address, 0), 1)
                soTimeout = ACCEPT_POLL_MS
            }
            val video = resources.track(ServerSocket()).apply {
                bind(InetSocketAddress(local.address, 0), 1)
                soTimeout = VIDEO_ACCEPT_MS
            }
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
            val raw = resources.track(acceptDirectPeer(run, control, local))
            post(run, resources) { onStatus(PairedConnectionStatus.CONNECTING) }
            val pairedSession = WiFiPairedSession(
                keyManager, hostPin
            )
            val established = pairedSession.acceptControl(raw, CAPABILITIES, HANDSHAKE_MS)
            resources.track(established.socket)
            val media = pairedSession.acceptVideo(resources.track(acceptDirectPeer(run, video, local)), established, HANDSHAKE_MS)
            resources.track(media)
            val link = WiFiControlLink(established.socket.inputStream, established.socket.outputStream, established.epoch)
            val linkSocket = LinkSocket(link) { resources.close() }
            // The control socket and the paired Wi-Fi endpoint stop being offered once the session ends.
            if (!run.publish { selector.updatePairedControl(linkSocket) }) return
            val target = waitForSurface(run)
            if (!run.isActive || !resources.isActive) return
            post(run, resources) {
                    onStreaming(true)
                    onStatus(PairedConnectionStatus.WAITING_PICTURE)
            }
            // The attach record used video-socket sequence 1; stats records continue from 2.
            val statsLink = WiFiControlLink(media.inputStream, media.outputStream, established.epoch)
            WiFiVideoPlayer.run(media.inputStream, { resources.close() }, link, established.limits, established.epoch, target, statsLink, linkProbe::sample,
                onPictureReady = { post(run, resources) { onPictureReady() } }) {
                !run.isActive || !wifiAllowed()
            }
        } finally {
            run.publish { selector.updatePairedControl(null) }
            post(run) { onStreaming(false) }
            watcher.interrupt()
            if (registered) try { nsd.unregisterService(registration) } catch (_: IllegalArgumentException) {}
            try { run.release(resources) } catch (error: IOException) { Log.w(TAG, "Close Wi-Fi session", error) }
        }
    }

    private fun post(run: WiFiSessionCancellation, session: WiFiSessionCancellation? = null, action: () -> Unit) {
        main.post {
            run.publish {
                if (session == null) action() else session.publish(action)
            }
        }
    }

    private fun waitForSurface(run: WiFiSessionCancellation): Surface {
        repeat(SURFACE_WAIT_POLLS) {
            if (!run.isActive) throw IOException("Wi-Fi session cancelled")
            surface()?.let { return it }
            Thread.sleep(100)
        }
        throw IOException("No video surface for Wi-Fi session")
    }

    private fun acceptDirectPeer(run: WiFiSessionCancellation, server: ServerSocket, local: LinkAddress): Socket {
        while (run.isActive) {
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
            maxWidth = 1920, maxHeight = 1200, maxFps = 60, h264Level = 50,
            maxAccessUnit = 512 * 1024, queueBytes = 1536 * 1024, queueSlots = 3,
            statsVersion = 1,
            features = WiFiSessionProtocol.FEATURE_LOW_LATENCY or WiFiSessionProtocol.FEATURE_VIDEO_STATS, spki = ByteArray(32), nonce = ByteArray(32)
        )
    }
}
