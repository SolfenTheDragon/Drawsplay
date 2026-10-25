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
import com.drawsplay.tablet.transport.WiFiTrustException
import com.drawsplay.tablet.transport.WiFiTrustFailure
import com.drawsplay.tablet.transport.WiFiTrustStore
import com.drawsplay.tablet.transport.WiFiSessionCancellation
import java.io.IOException
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.net.Socket
import java.net.SocketTimeoutException
import java.util.concurrent.CompletableFuture
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.TimeoutException
import java.util.concurrent.atomic.AtomicBoolean
import javax.net.ssl.X509KeyManager

/** One pairing attempt. All UI callbacks are on main; all socket work is off main. */
class WiFiPairingSession(
    context: Context,
    private val onStatus: (PairingStatus) -> Unit,
    private val onPaired: () -> Boolean = { true },
    private val onFinished: (paired: Boolean) -> Unit
) {
    private val app = context.applicationContext
    private val stopped = AtomicBoolean(false)
    private val code = CompletableFuture<String>()
    private val worker = Executors.newSingleThreadExecutor()
    private val main = Handler(Looper.getMainLooper())
    private val resources = WiFiSessionCancellation()
    @Volatile private var multicast: WifiManager.MulticastLock? = null
    @Volatile private var nsd: NsdManager? = null
    @Volatile private var registration: NsdManager.RegistrationListener? = null

    fun submitCode(entered: String): Boolean {
        if (stopped.get()) return false
        if (entered.length != 8 || entered.any { it !in '0'..'9' }) {
            onStatus(PairingStatus.INVALID_CODE)
            return false
        }
        if (!code.complete(entered)) return false
        onStatus(PairingStatus.CHECKING)
        return true
    }

    fun cancel() {
        release(false)
    }

    fun start() {
        worker.execute { enroll() }
    }

    private fun enroll() {
        var paired = false
        var failure = PairingStatus.NETWORK_UNAVAILABLE
        try {
            val (local, network) = WiFiLan.select(app) ?: throw IOException("No eligible Wi-Fi LAN")
            val listener = resources.track(ServerSocket()).apply {
                bind(InetSocketAddress(local.address, 0), 1)
                soTimeout = 1000
            }
            val placeholder = resources.track(ServerSocket()).apply { bind(InetSocketAddress(local.address, 0), 1) }
            resources.publish {
                multicast = (app.getSystemService(Context.WIFI_SERVICE) as WifiManager)
                    .createMulticastLock("DrawSplay-pairing").apply { setReferenceCounted(false); acquire() }
                val manager = app.getSystemService(Context.NSD_SERVICE) as NsdManager
                nsd = manager
                val callback = object : NsdManager.RegistrationListener {
                    override fun onServiceRegistered(info: NsdServiceInfo) {}
                    override fun onRegistrationFailed(info: NsdServiceInfo, error: Int) {
                        Log.w(TAG, "Pairing discovery registration failed: $error")
                        fail(PairingStatus.DISCOVERY_FAILED)
                    }
                    override fun onServiceUnregistered(info: NsdServiceInfo) {}
                    override fun onUnregistrationFailed(info: NsdServiceInfo, error: Int) { Log.w(TAG, "Pairing discovery unregister failed: $error") }
                }
                registration = callback
                manager.registerService(NsdServiceInfo().apply {
                    serviceName = "DrawSplay-${Build.MODEL}".take(48)
                    serviceType = "_drawsplay-tablet._tcp."
                    port = listener.localPort
                    if (Build.VERSION.SDK_INT >= 33) setNetwork(network)
                    setAttribute("v", "2")
                    setAttribute("videoPort", placeholder.localPort.toString())
                    setAttribute("pairing", "1")
                    setAttribute("streaming", "0")
                }, NsdManager.PROTOCOL_DNS_SD, callback)
            }
            failure = PairingStatus.EXPIRED
            val store = WiFiTrustStore(app)
            val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(120)
            var raw: Socket? = null
            while (raw == null && resources.isActive && System.nanoTime() < deadline) {
                val candidate = try { listener.accept() } catch (_: SocketTimeoutException) { continue }
                if (WiFiLanPolicy.isDirectPeer(local.address, local.prefixLength, candidate.inetAddress)) {
                    raw = resources.track(candidate)
                } else candidate.close()
            }
            if (raw == null) throw TimeoutException("Pairing listener expired")
            val remaining = deadline - System.nanoTime()
            if (remaining <= 0) throw TimeoutException("Pairing code expired")
            val entered = code.get(minOf(TimeUnit.SECONDS.toNanos(110), remaining), TimeUnit.NANOSECONDS)
            failure = PairingStatus.IDENTITY_REPAIR
            val keyManager = store.keyManagers().filterIsInstance<X509KeyManager>().single()
            failure = PairingStatus.VERIFICATION_FAILED
            val pending = WiFiEnrollment(keyManager).authenticateAccepted(raw, entered, 30000)
            resources.track(pending.socket).use { secured ->
                postStatus(PairingStatus.APPROVAL)
                failure = PairingStatus.NOT_APPROVED
                secured.soTimeout = 60000
                if (secured.inputStream.read() != 1) throw IOException("Computer did not approve pairing")
                postStatus(PairingStatus.SAVING)
                failure = PairingStatus.TRUST_REPAIR
                // Serialize saving with cancellation so a cancelled attempt cannot
                // restore trust after a user has forgotten the previous computer.
                if (!resources.publish { store.savePairedHost(pending.hostSpkiSha256, "Paired computer") }) {
                    throw IOException("Pairing cancelled")
                }
                failure = PairingStatus.FAILED
                WiFiPairingCompletion.confirm(secured.inputStream, secured.outputStream, onPaired)
            }
            paired = true
            postStatus(PairingStatus.PAIRED)
        } catch (error: Exception) {
            if (!stopped.get()) {
                Log.e(TAG, "Wi-Fi pairing failed at $failure", error)
                val status = when (error) {
                    is WiFiTrustException -> if (error.failure == WiFiTrustFailure.IDENTITY) PairingStatus.IDENTITY_REPAIR else PairingStatus.TRUST_REPAIR
                    is TimeoutException -> PairingStatus.EXPIRED
                    else -> failure
                }
                fail(status)
            }
        } finally {
            release(paired)
        }
    }

    private fun postStatus(status: PairingStatus) {
        main.post { onStatus(status) }
    }

    private fun fail(status: PairingStatus) {
        postStatus(status)
        release(false)
    }

    private fun release(paired: Boolean) {
        if (!stopped.compareAndSet(false, true)) return
        code.cancel(true)
        try { resources.close() } catch (error: IOException) { Log.w(TAG, "Close pairing resources", error) }
        registration?.let {
            try { nsd?.unregisterService(it) } catch (error: IllegalArgumentException) { Log.d(TAG, "Pairing registration already gone", error) }
        }
        multicast?.let { if (it.isHeld) it.release() }
        worker.shutdown()
        main.post { onFinished(paired) }
    }

    companion object {
        private const val TAG = "WiFiPairing"
    }
}
