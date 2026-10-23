package com.drawsplay.tablet

import android.app.Activity
import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.net.wifi.WifiManager
import android.os.Build
import android.os.Bundle
import android.text.InputType
import android.util.Log
import android.view.WindowManager
import android.view.Surface
import android.view.SurfaceHolder
import android.view.SurfaceView
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.TextView
import com.drawsplay.tablet.transport.WiFiEnrollment
import com.drawsplay.tablet.transport.WiFiPairedSession
import com.drawsplay.tablet.transport.WiFiSessionProtocol
import com.drawsplay.tablet.transport.WiFiTrustStore
import com.drawsplay.tablet.transport.WiFiFrameCodec
import com.drawsplay.tablet.video.WiFiVideoReceiver
import com.drawsplay.tablet.video.WiFiVideoDecoder
import java.nio.ByteBuffer
import java.io.IOException
import java.net.Inet4Address
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.net.Socket
import java.util.concurrent.CompletableFuture
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference
import javax.net.ssl.X509KeyManager

/** Test-only foreground LAN enrollment, never registered for USB attachments. */
class WiFiEnrollmentProbeActivity : Activity() {
    private val stopped = AtomicBoolean(false)
    private val code = CompletableFuture<String>()
    private val worker = Executors.newSingleThreadExecutor()
    private val timer = Executors.newSingleThreadScheduledExecutor()
    private var control: ServerSocket? = null
    private var video: ServerSocket? = null
    @Volatile private var active: Socket? = null
    @Volatile private var paired: WiFiPairedSession.Control? = null
    private var multicast: WifiManager.MulticastLock? = null
    private var nsd: NsdManager? = null
    private var registration: NsdManager.RegistrationListener? = null
    private var connectivity: ConnectivityManager? = null
    private var callback: ConnectivityManager.NetworkCallback? = null
    private lateinit var status: TextView
    private val renderSurface = CompletableFuture<Surface>()

    override fun onCreate(state: Bundle?) {
        super.onCreate(state)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        status = TextView(this).apply { text = "LAN enrollment test. No video or input access. Enter the host's code to approve it." }
        val entry = EditText(this).apply {
            hint = "Eight-digit code"
            inputType = InputType.TYPE_CLASS_NUMBER
            isSingleLine = true
        }
        val approve = Button(this).apply {
            text = "Pair and trust this host"
            setOnClickListener {
                val entered = entry.text.toString()
                if (entered.length != 8 || entered.any { it !in '0'..'9' }) {
                    status.text = "Enter exactly eight digits from the host."
                } else {
                    code.complete(entered)
                    entry.text.clear()
                    isEnabled = false
                    status.text = "Approving only the host that proves knowledge of this code..."
                }
            }
        }
        setContentView(LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            addView(status)
            addView(entry)
            addView(approve)
            if (intent.getBooleanExtra("checkVideo", false)) {
                addView(SurfaceView(this@WiFiEnrollmentProbeActivity).apply {
                    holder.addCallback(object : SurfaceHolder.Callback {
                        override fun surfaceCreated(holder: SurfaceHolder) { renderSurface.complete(holder.surface) }
                        override fun surfaceChanged(holder: SurfaceHolder, format: Int, width: Int, height: Int) {}
                        override fun surfaceDestroyed(holder: SurfaceHolder) { failProbe("Synthetic render surface closed") }
                    })
                }, LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, 400))
            }
        })
        try {
            if (Build.VERSION.SDK_INT < 33) throw IOException("This network-scoped NSD hardware probe requires API 33; enrollment codec itself supports API 26")
            val manager = getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
            connectivity = manager
            val networks = manager.allNetworks.filter { network ->
                manager.getNetworkCapabilities(network)?.let {
                    it.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) && !it.hasTransport(NetworkCapabilities.TRANSPORT_VPN)
                } == true
            }
            if (networks.size != 1) throw IOException("Select exactly one active Wi-Fi LAN; do not use a VPN for this probe")
            val network = networks.single()
            val link = manager.getLinkProperties(network) ?: throw IOException("Wi-Fi link addresses are unavailable")
            val selected = link.linkAddresses.filter { it.address is Inet4Address && !it.address.isLoopbackAddress }
            if (selected.size != 1) throw IOException("Probe requires exactly one IPv4 Wi-Fi address")
            val local = selected.single()
            control = ServerSocket().apply { bind(InetSocketAddress(local.address, 0), 1); soTimeout = 120000 }
            video = ServerSocket().apply { bind(InetSocketAddress(local.address, 0), 1); soTimeout = 10000 }
            val watcher = object : ConnectivityManager.NetworkCallback() {
                override fun onLost(lost: Network) {
                    if (lost == network) failProbe("Selected Wi-Fi disconnected")
                }
                override fun onLinkPropertiesChanged(changed: Network, properties: LinkProperties) {
                    if (changed == network && (properties.interfaceName != link.interfaceName || properties.linkAddresses != link.linkAddresses)) {
                        failProbe("Selected Wi-Fi addresses changed; reopen pairing")
                    }
                }
            }
            callback = watcher
            manager.registerNetworkCallback(
                NetworkRequest.Builder().addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
                    .removeCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET).build(), watcher
            )
            multicast = (applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager)
                .createMulticastLock("DrawSplay-enrollment-test").apply { setReferenceCounted(false); acquire() }
            nsd = getSystemService(Context.NSD_SERVICE) as NsdManager
            val listener = object : NsdManager.RegistrationListener {
                override fun onServiceRegistered(info: NsdServiceInfo) {
                    if (stopped.get()) return
                    Log.i(TAG, """{"state":"available","name":"${info.serviceName}","inputPort":${control?.localPort},"videoPort":${video?.localPort}}""")
                    runOnUiThread { if (!stopped.get()) status.text = "Host can discover ${info.serviceName}. Enter its eight-digit code, then approve pairing." }
                }
                override fun onRegistrationFailed(info: NsdServiceInfo, error: Int) = failProbe("NSD registration failed ($error)")
                override fun onServiceUnregistered(info: NsdServiceInfo) { Log.i(TAG, "Advertisement removed") }
                override fun onUnregistrationFailed(info: NsdServiceInfo, error: Int) { Log.e(TAG, "NSD cleanup failed ($error)") }
            }
            registration = listener
            nsd!!.registerService(NsdServiceInfo().apply {
                serviceName = "DrawSplay enrollment test"
                serviceType = "_drawsplay-tablet._tcp."
                port = control!!.localPort
                setNetwork(network)
                setAttribute("v", "2")
                setAttribute("videoPort", video!!.localPort.toString())
                setAttribute("pairing", "1")
                setAttribute("streaming", "0")
            }, NsdManager.PROTOCOL_DNS_SD, listener)
            timer.schedule({ failProbe("Enrollment window expired; reopen locally") }, 120, TimeUnit.SECONDS)
            val inputListener = control!!
            worker.execute {
                try {
                    val store = WiFiTrustStore(applicationContext)
                    if (store.readPairedHost() != null) throw IOException("Fresh isolated installation required; no existing trust is overwritten")
                    fun acceptSelectedPeer(listener: ServerSocket): Socket {
                        val raw = listener.accept()
                        active = raw
                        if (stopped.get()) throw IOException("Enrollment cancelled")
                        if (raw.localAddress != local.address || raw.inetAddress !is Inet4Address) {
                            throw IOException("Peer is not on selected IPv4 Wi-Fi")
                        }
                        val bits = local.prefixLength
                        if (bits !in 1..30) throw IOException("Unsupported Wi-Fi prefix")
                        fun ipNumber(bytes: ByteArray): Int = bytes.fold(0) { value, byte -> (value shl 8) or (byte.toInt() and 255) }
                        val mask = -1 shl (32 - bits)
                        val peer = ipNumber(raw.inetAddress.address)
                        val subnet = ipNumber(local.address.address) and mask
                        if ((peer and mask) != subnet || peer == subnet || peer == (subnet or mask.inv()) || raw.inetAddress == local.address) {
                            throw IOException("Peer is outside the selected directly connected LAN")
                        }
                        raw.tcpNoDelay = true
                        return raw
                    }
                    val raw = acceptSelectedPeer(inputListener)
                    Log.i(TAG, "Accepted directly connected selected-Wi-Fi peer; awaiting local code")
                    val entered = code.get(110, TimeUnit.SECONDS)
                    val identity = store.identitySpkiSha256()
                    val keyManager = store.keyManagers().filterIsInstance<X509KeyManager>().single()
                    val pending = WiFiEnrollment(keyManager).authenticateAccepted(raw, entered, 30000)
                    Log.i(TAG, "PAKE and both TLS identities verified; awaiting host approval")
                    pending.socket.use { secured ->
                        active = secured
                        secured.soTimeout = 60000
                        if (secured.inputStream.read() != 1) throw IOException("Host did not approve enrollment locally")
                        if (stopped.get()) throw IOException("Enrollment cancelled before saving trust")
                        store.savePairedHost(pending.hostSpkiSha256, "LAN test host")
                        secured.outputStream.write(1)
                        if (secured.inputStream.read() != 2) throw IOException("Tablet trust saved but host persistence was not confirmed; re-pair locally")
                        val reloaded = WiFiTrustStore(applicationContext)
                        check(reloaded.identitySpkiSha256().contentEquals(identity))
                        check(reloaded.readPairedHost()?.spkiSha256?.contentEquals(pending.hostSpkiSha256) == true)
                        Log.i(TAG, """{"state":"paired","api":${Build.VERSION.SDK_INT},"mutualTls":true,"trustReloaded":true,"identityRetained":true,"hostConfirmedPersistence":true}""")
                        runOnUiThread { status.text = "Paired over the selected LAN. Trust saved on both devices. No video/input session was started." }
                    }
                    if (intent.getBooleanExtra("checkSession", false)) {
                        inputListener.soTimeout = 10000
                        val saved = WiFiTrustStore(applicationContext)
                        val savedHost = saved.readPairedHost() ?: throw IOException("Saved host trust disappeared")
                        val session = WiFiPairedSession(
                            saved.keyManagers().filterIsInstance<X509KeyManager>().single(),
                            savedHost.spkiSha256
                        )
                        val capabilities = WiFiSessionProtocol.Hello(
                            minMajor = 2, maxMajor = 2, role = WiFiSessionProtocol.TABLET, codecMask = 1,
                            maxWidth = 1280, maxHeight = 720, maxFps = 30, h264Level = 31,
                            maxAccessUnit = 256 * 1024, queueBytes = 512 * 1024, queueSlots = 2,
                            statsVersion = 1, features = 1, spki = ByteArray(32), nonce = ByteArray(32)
                        )
                        session.acceptControl(acceptSelectedPeer(inputListener), capabilities, 5000).use { controlSession ->
                            paired = controlSession
                            if (stopped.get()) throw IOException("Session test cancelled")
                            val media = session.acceptVideo(acceptSelectedPeer(video!!), controlSession, 5000)
                            val limits = controlSession.limits
                            check(limits.maxWidth == 1280 && limits.maxHeight == 720 && limits.maxFps == 30)
                            check(limits.maxAccessUnit == 256 * 1024 && limits.queueBytes == 512 * 1024 && limits.queueSlots == 2)
                            Log.i(TAG, """{"state":"sessionReady","freshSavedTrust":true,"controlAndVideoMutualTls":true,"boundedCapabilities":true,"captureAndInputStarted":false}""")
                            runOnUiThread { status.text = "Saved trust authenticated fresh control/video channels. No screen capture or input injection was started." }
                            if (intent.getBooleanExtra("checkVideo", false)) {
                                checkSyntheticVideo(media, controlSession, controlSession.epoch, intent.getBooleanExtra("forceVideoRecovery", false))
                            }
                        }
                    }
                    stopNetwork()
                } catch (error: Exception) {
                    Log.e(TAG, "LAN enrollment failed; no display session authorized", error)
                    failProbe("Pairing failed: ${error.message}")
                }
            }
        } catch (error: Exception) {
            Log.e(TAG, "Cannot start isolated LAN enrollment", error)
            failProbe(error.message ?: "Cannot start LAN enrollment")
        }
    }

    private fun failProbe(message: String) {
        runOnUiThread { status.text = message }
        stopNetwork()
    }

    private fun checkSyntheticVideo(media: Socket, session: WiFiPairedSession.Control, epoch: Long, forceRecovery: Boolean) {
        media.soTimeout = 10000
        val receiver = WiFiVideoReceiver(media.inputStream, session.limits, epoch)
        val surface = renderSurface.get(5, TimeUnit.SECONDS)
        val ready = CompletableFuture<WiFiVideoDecoder>()
        val ended = AtomicBoolean(false)
        val failure = AtomicReference<Throwable>()
        val decoderThread = Thread({
            try {
                WiFiVideoDecoder(surface, receiver.config, receiver.queue, session.limits.maxAccessUnit).use { decoder ->
                    ready.complete(decoder)
                    var stalled = !forceRecovery
                    while (!WiFiVideoDecoder.drained(ended.get(), receiver.queue.depth, decoder.pendingFrames, decoder.holdingRecoverySlot)) {
                        if (stopped.get()) throw IOException("Wi-Fi synthetic decode cancelled")
                        // Test-only: pause the codec owner so the bounded queue overflows and must recover at an IDR.
                        if (!stalled && decoder.decoded >= 60) {
                            stalled = true
                            Thread.sleep(700)
                        }
                        val now = System.nanoTime() / 1000
                        if (!decoder.step(now)) Thread.sleep(1)
                        if (decoder.pendingFrames != 0 && now - decoder.lastOutputUs > 2000000) {
                            throw IOException("Wi-Fi decoder stalled for two seconds: received=${receiver.received} submitted=${decoder.submitted} decoded=${decoder.decoded} pending=${decoder.pendingFrames} queueDrops=${receiver.queue.dropped}")
                        }
                    }
                }
            } catch (error: Exception) {
                Log.e(TAG, "Synthetic decoder failed", error)
                failure.set(error)
                ready.completeExceptionally(error)
                try { media.close() } catch (closeError: IOException) { error.addSuppressed(closeError) }
            }
        }, "wifi-synthetic-decoder")
        decoderThread.start()
        try {
            val decoder = ready.get(5, TimeUnit.SECONDS)
            val controlWriter = WiFiFrameCodec.Writer(session.socket.outputStream)
            val header = WiFiFrameCodec.Header().apply {
                type = WiFiFrameCodec.CONTROL
                this.epoch = epoch
                sequence = 2
                payloadLength = 4
            }
            val result = ByteArray(24)
            result[0] = 7
            result[3] = 1
            controlWriter.writeFrame(header, result, 0, 4)
            while (receiver.receive()) {
                failure.get()?.let { throw IOException("Synthetic Wi-Fi decoder failed", it) }
            }
            ended.set(true)
            decoderThread.join(3000)
            if (decoderThread.isAlive) throw IOException("Synthetic decoder did not drain promptly")
            failure.get()?.let { throw IOException("Synthetic Wi-Fi decode failed", it) }
            ByteBuffer.wrap(result).apply {
                put(6).put(0).putShort(1)
                putInt(receiver.received.toInt()).putInt(decoder.decoded.toInt()).putInt(decoder.renderReleased.toInt())
                putInt(receiver.queue.dropped.toInt()).putInt(decoder.decodeDrops.toInt())
            }
            header.sequence = 3
            header.payloadLength = result.size
            controlWriter.writeFrame(header, result, 0, result.size)
            Log.i(TAG, """{"state":"videoComplete","received":${receiver.received},"decoded":${decoder.decoded},"renderReleased":${decoder.renderReleased},"queueDrops":${receiver.queue.dropped},"decodeDrops":${decoder.decodeDrops},"overflowEvents":${receiver.queue.overflowEvents},"ageEvents":${receiver.queue.ageEvents},"maxInterArrivalUs":${receiver.maxInterArrivalUs},"burstArrivals":${receiver.burstArrivals},"compressedPoolBytes":${receiver.queue.storageBytes},"decoder":"${decoder.name}","width":${receiver.config.width},"height":${receiver.config.height},"fpsTarget":${receiver.config.fps}}""")
            runOnUiThread { status.text = "Synthetic Wi-Fi AVC decoded and released ${decoder.renderReleased}/${receiver.received} pictures to Surface. No desktop content or input injection." }
        } finally {
            ended.set(true)
            if (decoderThread.isAlive) {
                decoderThread.interrupt()
                decoderThread.join(3000)
                if (decoderThread.isAlive) throw IOException("Wi-Fi decoder thread cleanup timed out")
            }
            receiver.queue.close()
        }
    }

    private fun stopNetwork() {
        if (!stopped.compareAndSet(false, true)) return
        code.cancel(false)
        for (resource in listOfNotNull(active, paired, control, video)) {
            try { resource.close() } catch (error: IOException) { Log.e(TAG, "Socket cleanup failed", error) }
        }
        callback?.let {
            try { connectivity?.unregisterNetworkCallback(it) } catch (error: IllegalArgumentException) { Log.e(TAG, "Network callback cleanup failed", error) }
        }
        registration?.let {
            try { nsd?.unregisterService(it) } catch (error: IllegalArgumentException) { Log.e(TAG, "NSD cleanup failed", error) }
        }
        multicast?.let { if (it.isHeld) it.release() }
        timer.shutdownNow()
        worker.shutdownNow()
        Log.i(TAG, "Enrollment sockets, callback and multicast lock released")
    }

    override fun onStop() {
        super.onStop()
        stopNetwork()
    }

    companion object {
        private const val TAG = "DS-WiFiEnrollment"
    }
}
