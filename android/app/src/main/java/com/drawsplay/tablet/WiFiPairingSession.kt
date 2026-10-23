package com.drawsplay.tablet

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.net.wifi.WifiManager
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.util.Log
import com.drawsplay.tablet.transport.WiFiEnrollment
import com.drawsplay.tablet.transport.WiFiLan
import com.drawsplay.tablet.transport.WiFiLanPolicy
import com.drawsplay.tablet.transport.WiFiTrustStore
import java.io.IOException
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.net.Socket
import java.net.SocketTimeoutException
import java.util.concurrent.CompletableFuture
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import javax.net.ssl.X509KeyManager

/**
 * Opt-in Wi-Fi pairing, driven by the "Wi-Fi connect" panel on the main screen.
 * It never starts a display session; callbacks arrive on the main thread.
 */
class WiFiPairingSession(
    private val context: Context,
    private val onStatus: (String) -> Unit,
    private val onFinished: (paired: Boolean) -> Unit
) {
    private val stopped = AtomicBoolean(false)
    private val code = CompletableFuture<String>()
    private val worker = Executors.newSingleThreadExecutor()
    private val main = Handler(Looper.getMainLooper())
    @Volatile private var server: ServerSocket? = null
    @Volatile private var placeholderVideo: ServerSocket? = null
    @Volatile private var active: Socket? = null
    private var multicast: WifiManager.MulticastLock? = null
    private var nsd: NsdManager? = null
    private var registration: NsdManager.RegistrationListener? = null

    /** Returns false (and reports why) when the code is not eight digits. */
    fun submitCode(entered: String): Boolean {
        if (entered.length != 8 || entered.any { it !in '0'..'9' }) {
            onStatus("Enter exactly eight digits from the computer.")
            return false
        }
        code.complete(entered)
        onStatus("Verifying the computer...")
        return true
    }

    fun cancel() = release(paired = false)

    fun start() {
        try {
            val (local, network) = WiFiLan.select(context) ?: throw IOException("Connect to exactly one Wi-Fi network (no VPN) to pair")
            val listener = ServerSocket().apply { bind(InetSocketAddress(local.address, 0), 1); soTimeout = 1000 }
            server = listener
            // The host requires distinct input and video ports even while pairing; no video is ever served here.
            val placeholder = ServerSocket().apply { bind(InetSocketAddress(local.address, 0), 1) }
            placeholderVideo = placeholder
            multicast = (context.applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager)
                .createMulticastLock("DrawSplay-pairing").apply { setReferenceCounted(false); acquire() }
            val manager = context.getSystemService(Context.NSD_SERVICE) as NsdManager
            nsd = manager
            val registrationListener = object : NsdManager.RegistrationListener {
                override fun onServiceRegistered(info: NsdServiceInfo) {}
                override fun onRegistrationFailed(info: NsdServiceInfo, error: Int) = fail("Discovery registration failed ($error)")
                override fun onServiceUnregistered(info: NsdServiceInfo) {}
                override fun onUnregistrationFailed(info: NsdServiceInfo, error: Int) {}
            }
            registration = registrationListener
            manager.registerService(NsdServiceInfo().apply {
                serviceName = "DrawSplay-${Build.MODEL}".take(48)
                serviceType = "_drawsplay-tablet._tcp."
                port = listener.localPort
                if (Build.VERSION.SDK_INT >= 33) setNetwork(network)
                setAttribute("v", "2")
                setAttribute("videoPort", placeholder.localPort.toString())
                setAttribute("pairing", "1")
                setAttribute("streaming", "0")
            }, NsdManager.PROTOCOL_DNS_SD, registrationListener)
            worker.execute { enroll(listener, local.address, local.prefixLength) }
        } catch (error: Exception) {
            Log.e(TAG, "Cannot start Wi-Fi pairing", error)
            fail(error.message ?: "Cannot start Wi-Fi pairing")
        }
    }

    private fun enroll(listener: ServerSocket, local: java.net.InetAddress, prefix: Int) {
        var paired = false
        try {
            val store = WiFiTrustStore(context.applicationContext)
            val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(120)
            var raw: Socket? = null
            while (raw == null && !stopped.get() && System.nanoTime() < deadline) {
                val candidate = try { listener.accept() } catch (_: SocketTimeoutException) { continue }
                if (WiFiLanPolicy.isDirectPeer(local, prefix, candidate.inetAddress)) raw = candidate else candidate.close()
            }
            if (raw == null) throw IOException("Pairing window ended; reopen pairing")
            active = raw
            val entered = code.get(110, TimeUnit.SECONDS)
            val keyManager = store.keyManagers().filterIsInstance<X509KeyManager>().single()
            val pending = WiFiEnrollment(keyManager).authenticateAccepted(raw, entered, 30000)
            pending.socket.use { secured ->
                active = secured
                secured.soTimeout = 60000
                if (secured.inputStream.read() != 1) throw IOException("The computer did not approve pairing")
                if (stopped.get()) throw IOException("Pairing cancelled")
                store.savePairedHost(pending.hostSpkiSha256, "Paired computer")
                secured.outputStream.write(1)
                if (secured.inputStream.read() != 2) throw IOException("Computer did not confirm; pair again")
            }
            onStatusMain("Paired. Unplug USB (or turn off adb) and the tablet connects over Wi-Fi.")
            paired = true
        } catch (error: Exception) {
            Log.e(TAG, "Wi-Fi pairing failed", error)
            fail("Pairing failed: ${error.message}")
        } finally {
            release(paired)
        }
    }

    private fun onStatusMain(message: String) = main.post { onStatus(message) }

    private fun fail(message: String) {
        onStatusMain(message)
        release(paired = false)
    }

    private fun release(paired: Boolean) {
        if (!stopped.compareAndSet(false, true)) return
        for (resource in listOfNotNull(server, placeholderVideo, active)) {
            try { resource.close() } catch (_: IOException) {}
        }
        registration?.let { try { nsd?.unregisterService(it) } catch (_: IllegalArgumentException) {} }
        multicast?.let { if (it.isHeld) it.release() }
        worker.shutdown()
        main.post { onFinished(paired) }
    }

    companion object {
        private const val TAG = "WiFiPairing"
    }
}
