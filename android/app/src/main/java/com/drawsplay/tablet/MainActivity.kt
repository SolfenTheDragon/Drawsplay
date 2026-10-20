package com.drawsplay.tablet

import com.drawsplay.tablet.video.TileView
import android.content.Intent
import android.content.SharedPreferences
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.util.Log
import android.view.Display
import android.view.Gravity
import android.view.View
import android.graphics.ImageFormat
import android.view.KeyEvent
import android.view.Surface
import android.view.SurfaceHolder
import android.view.SurfaceView
import android.view.WindowManager
import android.widget.FrameLayout
import android.widget.TextView
import android.media.ImageReader
import androidx.appcompat.app.AppCompatActivity
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.security.crypto.EncryptedSharedPreferences
import androidx.security.crypto.MasterKey
import com.drawsplay.tablet.input.InputChannel
import com.drawsplay.tablet.input.InputPadView
import com.drawsplay.tablet.transport.AccessoryTransport
import com.drawsplay.tablet.transport.HostEndpointSelector
import com.drawsplay.tablet.transport.WiFiDiscoveryManager
import com.drawsplay.tablet.video.VideoChannel
import com.drawsplay.tablet.video.VideoScaling

/**
 * Single fullscreen activity. Zero-configuration by design: both channels
 * connect automatically, retry until the host appears, and reconnect after
 * drops â€” there are no connect/disconnect controls.
 *
 * View stack (back to front):
 *  1. [SurfaceView]   â€” hardware-decoded host video, sized and centred
 *                       inside videoFrame per the host's [VideoScaling].
 *  2. [InputPadView]  â€” transparent, captures touch/stylus over the video so
 *                       input coordinates map 1:1 onto displayed host pixels.
 *  3. Status text     â€” only visible while waiting for the host.
 *
 * Channel lifecycles:
 *  - By default, input runs from onStart to onStop and video tracks the
 *    foreground surface.
 *  - When the host enables keepAliveOnBackground, input stays connected and
 *    video switches to a tiny headless sink when the foreground surface is
 *    destroyed, keeping the host monitor session alive while the app is
 *    backgrounded.
 */
class MainActivity : AppCompatActivity() {

    private lateinit var videoFrame: FrameLayout
    private lateinit var videoSurface: SurfaceView
    private lateinit var tileView: TileView
    private lateinit var statusOverlay: FrameLayout
    private lateinit var statusText: TextView
    private lateinit var inputPad: InputPadView

    private lateinit var inputChannel: InputChannel
    private lateinit var videoChannel: VideoChannel
    private val endpointSelector = HostEndpointSelector()
    private lateinit var wifiDiscoveryManager: WiFiDiscoveryManager
    private var backgroundSink: ImageReader? = null
    @Volatile
    private var pendingForegroundSurface: Surface? = null

    @Volatile
    private var appForeground = false

    @Volatile
    private var keepAliveOnBackground = false

    // UI thread only.
    private var videoScaling = VideoScaling.DEFAULT
    private var videoWidth = 0
    private var videoHeight = 0
    @Volatile
    private var wifiPolicyEnabled = false
    @Volatile
    private var wifiPolicyAuthToken = ""
    @Volatile
    private var wifiPolicyTlsRequired = false
    @Volatile
    private var wifiPolicyTlsFingerprint = ""

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        videoFrame = findViewById(R.id.videoFrame)
        videoSurface = findViewById(R.id.videoSurface)
        tileView = findViewById(R.id.tileView)
        statusOverlay = findViewById(R.id.statusOverlay)
        statusText = findViewById(R.id.statusText)
        inputPad = findViewById(R.id.inputPad)

        Log.i(TAG, "Android runtime profile: sdk=${AndroidCompat.sdkInt}, " +
            "windowMetrics=${AndroidCompat.supportsWindowMetrics}, " +
            "frameRateHint=${AndroidCompat.supportsSurfaceFrameRateHint}, " +
            "lowLatencyHint=${AndroidCompat.supportsLowLatencyDecoderHint}")

        enterImmersiveMode()
        requestHighRefreshMode()
        // A monitor must not sleep while in use.
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        restoreKnownHostPolicy()
        videoScaling = VideoScaling.fromWireName(displayPrefs.getString(KEY_VIDEO_SCALING, null))
            ?: VideoScaling.DEFAULT
        (videoFrame.parent as View).addOnLayoutChangeListener { _, l, t, r, b, ol, ot, or, ob ->
            if (r - l != or - ol || b - t != ob - ot) applyVideoLayout()
        }

        inputChannel = InputChannel(object : InputChannel.InputChannelListener {
            override fun onConnected() {
                runOnUiThread { updateStatus() }
            }

            override fun onWaitingForHost(reason: String?) {
                currentHostId = null
                runOnUiThread { updateStatus() }
            }

            override fun onHostDisplayMode(width: Int, height: Int, fps: Int) {
                storeDisplayModePreference(width, height, fps)
            }

            override fun onHostInfo(hostId: String) {
                currentHostId = hostId
                val prefs = loadDisplayModePreference(hostId)
                inputChannel.sendModePrefs(prefs.first, prefs.second, prefs.third)
            }

            override fun onClearHostDisplayModePreference() {
                clearDisplayModePreference()
            }

            override fun onSessionPolicy(
                keepAliveOnBackground: Boolean,
                videoScaling: String,
                wifiEnabled: Boolean,
                wifiAuthToken: String,
                wifiTlsRequired: Boolean,
                wifiTlsFingerprint: String,
                penPrediction: Boolean
            ) {
                inputPad.penPredictionEnabled = penPrediction
                applySessionPolicy(
                    keepAliveOnBackground,
                    wifiEnabled,
                    wifiAuthToken,
                    wifiTlsRequired,
                    wifiTlsFingerprint
                )
                runOnUiThread {
                    updateWiFiDiscoveryState()
                    VideoScaling.fromWireName(videoScaling)?.let { setVideoScaling(it) }
                }
            }
        }, endpointSelector, appVersionName())
        inputChannel.displayInfo = buildDisplayInfo()

        videoChannel = VideoChannel(object : VideoChannel.VideoChannelListener {
            override fun onStreamStarted(encoder: String, fps: Int) {
                streamUp = true
                runOnUiThread { updateStatus() }
            }

            override fun onWaitingForHost(reason: String?) {
                streamUp = false
                runOnUiThread { updateStatus() }
            }

            override fun onTileLayerVisible(visible: Boolean) {
                runOnUiThread {
                    tileView.visibility = if (visible) View.VISIBLE else View.GONE
                    if (!visible) tileView.clear()
                }
            }

            override fun onVideoSize(width: Int, height: Int) {
                runOnUiThread {
                    videoWidth = width
                    videoHeight = height
                    applyVideoLayout()
                }
            }
        }, endpointSelector) { appForeground }
        wifiDiscoveryManager = WiFiDiscoveryManager(
            this,
            endpointSelector,
            { wifiPolicyAuthToken },
            { wifiPolicyTlsRequired },
            { wifiPolicyTlsFingerprint }
        )

        videoChannel.tileSink = tileView
        inputPad.onInputBatch = { events -> inputChannel.send(events) }

        AccessoryTransport.start(this) { attached -> endpointSelector.updateAccessoryAttached(attached) }

        videoSurface.holder.addCallback(object : SurfaceHolder.Callback {
            override fun surfaceCreated(holder: SurfaceHolder) {
                pendingForegroundSurface = holder.surface
                releaseBackgroundVideoSink()
                maybeStartForegroundVideo()
            }

            override fun surfaceChanged(holder: SurfaceHolder, format: Int, width: Int, height: Int) {
                // Decoder output size is stream-driven; nothing to do here.
            }

            override fun surfaceDestroyed(holder: SurfaceHolder) {
                pendingForegroundSurface = null
                if (keepAliveOnBackground && !isFinishing) {
                    val sink = ensureBackgroundVideoSink()
                    if (sink != null) {
                        videoChannel.start(sink.surface)
                        return
                    }
                }
                videoChannel.stop()
            }
        })
    }

    override fun onStart() {
        super.onStart()
        appForeground = true
        updateWiFiDiscoveryState()
        AccessoryTransport.refresh()
        AccessoryTransport.setPolling(true)
        inputChannel.start()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        AccessoryTransport.refresh()
    }

    override fun onStop() {
        appForeground = false
        AccessoryTransport.setPolling(false)
        if (!keepAliveOnBackground || isFinishing) {
            inputChannel.stop()
        }
        wifiDiscoveryManager.stop()
        super.onStop()
    }

    override fun onDestroy() {
        AccessoryTransport.stop()
        releaseBackgroundVideoSink()
        videoChannel.stop()
        inputChannel.shutdown()
        super.onDestroy()
    }

    @Volatile
    private var streamUp = false

    // --- Host display mode (resolution matching) ---
    //
    // The hello reports the panel's native resolution (landscape-normalized)
    // so the host sizes its virtual display to match by default, plus the
    // stored preferred mode. When the user changes the resolution in the
    // Windows display settings, the host pushes it back and it is persisted
    // here â€” the app is the durable store for the user's choice.

    private val displayPrefs by lazy { getSharedPreferences("display", MODE_PRIVATE) }
    private val knownHostPrefs by lazy { knownHostPreferences() }

    private fun buildDisplayInfo(): InputChannel.DisplayInfo {
        val mode = currentDisplay()?.mode
        val fallbackBounds =
            if (AndroidCompat.supportsWindowMetrics) {
                windowManager.maximumWindowMetrics.bounds
            } else {
                @Suppress("DEPRECATION")
                resources.displayMetrics.run { android.graphics.Rect(0, 0, widthPixels, heightPixels) }
            }
        val physicalWidth = mode?.physicalWidth ?: fallbackBounds.width()
        val physicalHeight = mode?.physicalHeight ?: fallbackBounds.height()
        val nativeW = maxOf(physicalWidth, physicalHeight)
        val nativeH = minOf(physicalWidth, physicalHeight)
        val refreshRates = currentDisplay()?.supportedModes
            ?.filter { maxOf(it.physicalWidth, it.physicalHeight) == nativeW && minOf(it.physicalWidth, it.physicalHeight) == nativeH }
            ?.map { Math.round(it.refreshRate) }
            ?.filter { it > 0 }
            ?.distinct()
            ?.sorted()
            .orEmpty()
        return InputChannel.DisplayInfo(
            nativeWidth = nativeW,
            nativeHeight = nativeH,
            prefWidth = 0,
            prefHeight = 0,
            refreshRates = refreshRates
        )
    }

    // Display mode preferences are stored per host so two computers never share a choice.
    @Volatile
    private var currentHostId: String? = null

    private fun prefKey(hostId: String, field: String) = "host.$hostId.$field"

    private fun loadDisplayModePreference(hostId: String): Triple<Int, Int, Int> = Triple(
        displayPrefs.getInt(prefKey(hostId, "width"), 0),
        displayPrefs.getInt(prefKey(hostId, "height"), 0),
        displayPrefs.getInt(prefKey(hostId, "fps"), 0)
    )

    private fun storeDisplayModePreference(width: Int, height: Int, fps: Int) {
        val hostId = currentHostId ?: return
        // fps == 0 means the host only changed the resolution; keep the stored rate.
        val editor = displayPrefs.edit()
            .putInt(prefKey(hostId, "width"), width)
            .putInt(prefKey(hostId, "height"), height)
        if (fps > 0) editor.putInt(prefKey(hostId, "fps"), fps)
        if (!editor.commit()) {
            Log.w(TAG, "Failed to persist preferred display mode ${width}x${height}")
        }
    }

    private fun clearDisplayModePreference() {
        val hostId = currentHostId ?: return
        val saved = displayPrefs.edit()
            .remove(prefKey(hostId, "width"))
            .remove(prefKey(hostId, "height"))
            .remove(prefKey(hostId, "fps"))
            .commit()
        if (!saved) {
            Log.w(TAG, "Failed to clear preferred display mode")
        }
    }

    // --- Video scaling ---
    //
    // The surface and input pad share videoFrame, so resizing
    // it keeps touch/pen coordinates mapped onto the displayed picture. The
    // mode is remembered so the last choice applies before the host's policy
    // arrives on the next launch.

    private fun setVideoScaling(mode: VideoScaling) {
        if (mode == videoScaling) return
        videoScaling = mode
        displayPrefs.edit().putString(KEY_VIDEO_SCALING, mode.wireName).apply()
        applyVideoLayout()
    }

    private fun applyVideoLayout() {
        val root = videoFrame.parent as View
        val size = videoScaling.frameSize(videoWidth, videoHeight, root.width, root.height)
        val width = size?.first ?: FrameLayout.LayoutParams.MATCH_PARENT
        val height = size?.second ?: FrameLayout.LayoutParams.MATCH_PARENT
        val lp = videoFrame.layoutParams as FrameLayout.LayoutParams
        if (lp.width == width && lp.height == height) return
        lp.width = width
        lp.height = height
        lp.gravity = Gravity.CENTER
        videoFrame.layoutParams = lp
        Log.i(TAG, "Video scaling ${videoScaling.wireName}: ${videoWidth}x$videoHeight -> " +
            "${size?.let { "${it.first}x${it.second}" } ?: "full screen"}")
    }

    // --- Keyboard passthrough (M5) ---
    //
    // Physical keyboards attached to the tablet type on the host. Key
    // events are forwarded and consumed here so the tablet UI never reacts
    // to them; device-local keys (back, volume, power) stay local.

    override fun dispatchKeyEvent(event: KeyEvent): Boolean {
        if (isLocalKey(event.keyCode)) return super.dispatchKeyEvent(event)
        val action = when (event.action) {
            KeyEvent.ACTION_DOWN -> "down"
            KeyEvent.ACTION_UP -> "up"
            else -> return super.dispatchKeyEvent(event)
        }
        val chars = event.unicodeChar.takeIf { it != 0 }
            ?.let { String(Character.toChars(it and Int.MAX_VALUE)) }
        inputChannel.sendKey(action, event.keyCode, event.metaState, chars)
        return true
    }

    private fun isLocalKey(keyCode: Int): Boolean = when (keyCode) {
        KeyEvent.KEYCODE_BACK,
        KeyEvent.KEYCODE_HOME,
        KeyEvent.KEYCODE_APP_SWITCH,
        KeyEvent.KEYCODE_POWER,
        KeyEvent.KEYCODE_VOLUME_UP,
        KeyEvent.KEYCODE_VOLUME_DOWN,
        KeyEvent.KEYCODE_VOLUME_MUTE -> true
        else -> false
    }

    /**
     * Shows the opaque waiting overlay until video is flowing; hides it
     * after. Opaque so a stale last frame never lingers when the host
     * disconnects.
     */
    private fun updateStatus() {
        if (streamUp) {
            statusOverlay.visibility = FrameLayout.GONE
        } else {
            statusOverlay.visibility = FrameLayout.VISIBLE
            statusText.text = getString(R.string.status_waiting_for_host)
        }
    }

    /**
     * Holds the tablet panel at its highest mode while DrawSplay is active.
     *
     * Some adaptive-refresh Android builds (notably Samsung/MediaTek) apply
     * an "idle" override that drops a SurfaceView to 30 Hz even when the
     * surface explicitly votes for a fixed 60 fps source. The decoder still
     * reports 60 rendered frames per second in that state, but SurfaceFlinger
     * presents only half of them, which feels like a transport regression.
     * A window-level mode request is stronger than the surface hint and also
     * keeps pen/touch presentation latency low.
     */
    private fun requestHighRefreshMode() {
        val panel = currentDisplay() ?: return
        val current = panel.mode
        // Only modes at the panel's current resolution: a faster mode at a
        // lower resolution would silently downscale the picture.
        val bestMode = panel.supportedModes
            .filter { it.physicalWidth == current.physicalWidth && it.physicalHeight == current.physicalHeight }
            .maxByOrNull { it.refreshRate } ?: return
        val attrs = window.attributes
        attrs.preferredDisplayModeId = bestMode.modeId
        attrs.preferredRefreshRate = bestMode.refreshRate
        window.attributes = attrs
        Log.i(TAG, "Requested display mode ${bestMode.modeId} at ${bestMode.refreshRate} Hz")
    }

    private fun currentDisplay(): Display? =
        if (AndroidCompat.supportsWindowMetrics) {
            display
        } else {
            @Suppress("DEPRECATION")
            windowManager.defaultDisplay
        }

    private fun ensureBackgroundVideoSink(): ImageReader? {
        val existing = backgroundSink
        if (existing != null) return existing
        return try {
            val created = ImageReader.newInstance(16, 16, ImageFormat.PRIVATE, 2)
            backgroundSink = created
            created
        } catch (e: Exception) {
            Log.w(TAG, "Failed creating background video sink", e)
            null
        }
    }

    private fun releaseBackgroundVideoSink() {
        backgroundSink?.close()
        backgroundSink = null
    }

    private fun applySessionPolicy(
        keepAliveOnBackground: Boolean,
        wifiEnabled: Boolean,
        wifiAuthToken: String,
        wifiTlsRequired: Boolean,
        wifiTlsFingerprint: String
    ) {
        this.keepAliveOnBackground = keepAliveOnBackground
        this.wifiPolicyEnabled = wifiEnabled
        this.wifiPolicyTlsRequired = wifiTlsRequired
        val authToken = wifiAuthToken.trim()
        if (authToken.isNotEmpty()) {
            this.wifiPolicyAuthToken = authToken
        }
        val tlsFingerprint = wifiTlsFingerprint.trim()
        if (tlsFingerprint.isNotEmpty()) {
            this.wifiPolicyTlsFingerprint = tlsFingerprint
        }
        persistKnownHostPolicy()
        if (this.wifiPolicyEnabled && !hasKnownHostTrust()) {
            Log.w(TAG, "Host enabled Wi-Fi without complete known-host trust material")
        }
    }

    private fun restoreKnownHostPolicy() {
        wifiPolicyEnabled = knownHostPrefs.getBoolean(KEY_WIFI_ENABLED, false)
        wifiPolicyAuthToken = knownHostPrefs.getString(KEY_WIFI_AUTH_TOKEN, "")?.trim().orEmpty()
        wifiPolicyTlsRequired = knownHostPrefs.getBoolean(KEY_WIFI_TLS_REQUIRED, false)
        wifiPolicyTlsFingerprint =
            knownHostPrefs.getString(KEY_WIFI_TLS_FINGERPRINT, "")?.trim().orEmpty()
        if (wifiPolicyEnabled && !hasKnownHostTrust()) {
            Log.w(TAG, "Known-host trust was incomplete; waiting for fresh host policy")
            wifiPolicyEnabled = false
        }
    }

    private fun persistKnownHostPolicy() {
        val saved = knownHostPrefs.edit()
            .putBoolean(KEY_WIFI_ENABLED, wifiPolicyEnabled)
            .putString(KEY_WIFI_AUTH_TOKEN, wifiPolicyAuthToken)
            .putBoolean(KEY_WIFI_TLS_REQUIRED, wifiPolicyTlsRequired)
            .putString(KEY_WIFI_TLS_FINGERPRINT, wifiPolicyTlsFingerprint)
            .commit()
        if (!saved) {
            Log.w(TAG, "Failed to persist known-host Wi-Fi policy")
        }
    }

    private fun hasKnownHostTrust(): Boolean {
        if (wifiPolicyAuthToken.isBlank()) return false
        if (wifiPolicyTlsRequired && wifiPolicyTlsFingerprint.isBlank()) return false
        return true
    }

    private fun updateWiFiDiscoveryState() {
        if (wifiPolicyEnabled && hasKnownHostTrust()) {
            wifiDiscoveryManager.start()
        } else {
            wifiDiscoveryManager.stop()
        }
    }

    private fun maybeStartForegroundVideo() {
        val surface = pendingForegroundSurface ?: return
        videoChannel.start(surface)
    }

    private fun knownHostPreferences(): SharedPreferences {
        return try {
            val masterKey = MasterKey.Builder(this)
                .setKeyScheme(MasterKey.KeyScheme.AES256_GCM)
                .build()
            EncryptedSharedPreferences.create(
                this,
                "known-host",
                masterKey,
                EncryptedSharedPreferences.PrefKeyEncryptionScheme.AES256_SIV,
                EncryptedSharedPreferences.PrefValueEncryptionScheme.AES256_GCM
            )
        } catch (e: Exception) {
            Log.w(TAG, "Encrypted known-host preferences unavailable; falling back to MODE_PRIVATE", e)
            getSharedPreferences("known-host", MODE_PRIVATE)
        }
    }

    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        // Older Android versions bring the bars back after focus changes.
        if (hasFocus) enterImmersiveMode()
    }

    /** Hides system bars: a monitor shows only the host's pixels. */
    private fun enterImmersiveMode() {
        WindowCompat.setDecorFitsSystemWindows(window, false)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            window.attributes = window.attributes.apply {
                layoutInDisplayCutoutMode =
                    WindowManager.LayoutParams.LAYOUT_IN_DISPLAY_CUTOUT_MODE_SHORT_EDGES
            }
        }
        val controller = WindowInsetsControllerCompat(window, window.decorView)
        controller.hide(WindowInsetsCompat.Type.systemBars())
        controller.systemBarsBehavior =
            WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
    }

    companion object {
        private const val TAG = "MainActivity"
        private const val KEY_VIDEO_SCALING = "videoScaling"
        private const val KEY_WIFI_ENABLED = "wifiEnabled"
        private const val KEY_WIFI_AUTH_TOKEN = "wifiAuthToken"
        private const val KEY_WIFI_TLS_REQUIRED = "wifiTlsRequired"
        private const val KEY_WIFI_TLS_FINGERPRINT = "wifiTlsFingerprint"
    }

    private fun appVersionName(): String {
        return try {
            val pkgInfo =
                if (AndroidCompat.supportsPackageInfoFlagsApi) {
                    packageManager.getPackageInfo(
                        packageName,
                        PackageManager.PackageInfoFlags.of(0)
                    )
                } else {
                    @Suppress("DEPRECATION")
                    packageManager.getPackageInfo(packageName, 0)
                }
            pkgInfo.versionName ?: "unknown"
        } catch (_: Exception) {
            "unknown"
        }
    }
}
