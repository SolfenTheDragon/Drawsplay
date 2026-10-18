package com.drawsplay.tablet.input

import com.drawsplay.tablet.video.EchoTracker
import android.os.Build
import android.os.Process
import android.media.MediaCodecList
import android.media.MediaFormat
import com.drawsplay.tablet.AndroidCompat
import com.drawsplay.tablet.transport.AccessoryFraming
import com.drawsplay.tablet.transport.AccessoryTransport
import com.drawsplay.tablet.transport.HostEndpoint
import com.drawsplay.tablet.transport.HostEndpointSelector
import com.drawsplay.tablet.transport.TlsSocketFactory
import com.drawsplay.tablet.transport.UsbMuxFrameCodec
import org.json.JSONArray
import org.json.JSONObject
import java.io.OutputStream
import java.net.InetSocketAddress
import java.net.Socket
import java.util.concurrent.Callable
import java.util.concurrent.ExecutorCompletionService
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicLong
import java.util.concurrent.atomic.AtomicReference

class InputChannel(
    private val listener: InputChannelListener,
    private val endpointSelector: HostEndpointSelector,
    private val appVersion: String
) {

    interface InputChannelListener {
        fun onConnected()
        fun onWaitingForHost(reason: String?)
        fun onHostDisplayMode(width: Int, height: Int, fps: Int)
        fun onClearHostDisplayModePreference()
        /** The host identified itself; the app answers with [sendModePrefs] for that host. */
        fun onHostInfo(hostId: String)
        fun onSessionPolicy(
            keepAliveOnBackground: Boolean,
            videoScaling: String,
            wifiEnabled: Boolean,
            wifiAuthToken: String,
            wifiTlsRequired: Boolean,
            wifiTlsFingerprint: String,
            penPrediction: Boolean
        )
    }

    data class DisplayInfo(
        val nativeWidth: Int,
        val nativeHeight: Int,
        val prefWidth: Int,
        val prefHeight: Int,
        val prefFps: Int = 0,
        // Refresh rates (Hz, rounded, ascending) the panel supports at its native size.
        val refreshRates: List<Int> = emptyList()
    )

    @Volatile
    var displayInfo: DisplayInfo? = null

    private enum class ConnectionState { STOPPED, WAITING, CONNECTED }

    private enum class MoveKind {
        /** Not coalescible (down/up/button/key...): always delivered in order. */
        BOUNDARY,
        /** Hover: only the latest position matters. */
        LATEST_ONLY,
        /** Contact stroke: queued samples are merged so stroke resolution survives a backlog. */
        MERGE
    }

    private sealed class Outbound {
        abstract val generation: Long
        abstract val streamId: Byte

        /** Pre-serialized control/key payloads. */
        class Raw(
            override val generation: Long,
            val payloads: List<ByteArray>,
            override val streamId: Byte
        ) : Outbound()

        /**
         * Input samples; JSON serialization is deferred to the IO thread so the
         * UI thread only enqueues.
         */
        class Input(
            override val generation: Long,
            val events: List<InputEventPacket>,
            val action: String,
            val kind: MoveKind
        ) : Outbound() {
            override val streamId: Byte = UsbMuxFrameCodec.STREAM_INPUT
        }
    }

    // The IO thread carries every pen sample; run it at display priority so
    // it is not delayed behind background work between the UI handoff and the
    // socket write.
    private val ioExecutor = Executors.newSingleThreadScheduledExecutor { runnable ->
        Thread({
            try {
                Process.setThreadPriority(Process.THREAD_PRIORITY_URGENT_DISPLAY)
            } catch (_: Exception) {
            }
            runnable.run()
        }, "input-io")
    }
    private val lifecycleLock = Any()
    private val state = AtomicReference(ConnectionState.STOPPED)
    private val connectGeneration = AtomicLong(0L)

    private val queueLock = Any()
    private val sendQueue = ArrayDeque<Outbound>()
    private var drainScheduled = false

    private var socket: Socket? = null
    private var output: OutputStream? = null
    private var nextTxSeq = 1u

    @Volatile
    private var activeEndpoint: HostEndpoint? = null

    fun start() {
        if (!state.compareAndSet(ConnectionState.STOPPED, ConnectionState.WAITING)) return
        val generation = connectGeneration.incrementAndGet()
        listener.onWaitingForHost(null)
        ioExecutor.execute { connectAttemptOnIoThread(generation) }
    }

    fun stop() {
        synchronized(lifecycleLock) {
            connectGeneration.incrementAndGet()
            state.set(ConnectionState.STOPPED)
            closeActiveConnectionNow()
        }
        clearSendQueue()
    }

    fun shutdown() {
        stop()
        ioExecutor.shutdownNow()
    }

    private fun connectAttemptOnIoThread(generation: Long) {
        if (generation != connectGeneration.get() || state.get() != ConnectionState.WAITING) return

        var newSocket: Socket? = null
        try {
            val endpoints = endpointSelector.inputCandidates()
            val dialed = dialEndpointHappyEyeballs(endpoints, FALLBACK_RACE_DELAY_MS) { endpoint ->
                if (endpoint.accessory) {
                    return@dialEndpointHappyEyeballs AccessoryTransport.connect(
                        AccessoryFraming.KIND_INPUT,
                        CONNECT_TIMEOUT_MS.toLong()
                    )
                }
                if (endpoint.tls && endpoint.tlsFingerprintSha256.isNotBlank()) {
                    TlsSocketFactory.connectPinned(
                        endpoint.host,
                        endpoint.inputPort,
                        CONNECT_TIMEOUT_MS,
                        endpoint.tlsFingerprintSha256,
                        sendBufferSize = 128 * 1024,
                        receiveBufferSize = 128 * 1024
                    )
                } else {
                    Socket().apply {
                        sendBufferSize = 128 * 1024
                        receiveBufferSize = 128 * 1024
                        connect(InetSocketAddress(endpoint.host, endpoint.inputPort), CONNECT_TIMEOUT_MS)
                    }
                }.apply {
                    tcpNoDelay = true
                    keepAlive = true
                }
            }
            newSocket = dialed.first
            val selected = dialed.second
            val newOutput = newSocket.getOutputStream()

            synchronized(lifecycleLock) {
                if (generation != connectGeneration.get() || state.get() != ConnectionState.WAITING) {
                    closeQuietly(newOutput, newSocket)
                    return
                }
                closeActiveConnectionNow()
                socket = newSocket
                output = newOutput
                activeEndpoint = selected
                nextTxSeq = 1u
                state.set(ConnectionState.CONNECTED)
            }

            startReaderThread(newSocket, generation)
            writePayloadsOnIoThread(
                streamId = UsbMuxFrameCodec.STREAM_CONTROL,
                payloads = listOf(buildHelloPayload())
            )
            if (generation == connectGeneration.get() && state.get() == ConnectionState.CONNECTED) {
                listener.onConnected()
            }
        } catch (e: Exception) {
            synchronized(lifecycleLock) {
                if (generation != connectGeneration.get()) return
                closeQuietly(output, newSocket)
                closeActiveConnectionNow()
                if (state.get() == ConnectionState.STOPPED) return
                state.set(ConnectionState.WAITING)
            }
            clearSendQueue()
            scheduleReconnect(generation)
        }
    }

    private fun scheduleReconnect(generation: Long) {
        if (generation != connectGeneration.get()) return
        ioExecutor.schedule(
            { connectAttemptOnIoThread(generation) },
            RECONNECT_DELAY_MS,
            TimeUnit.MILLISECONDS
        )
    }

    fun send(events: List<InputEventPacket>) {
        if (state.get() != ConnectionState.CONNECTED || events.isEmpty()) return
        val generation = connectGeneration.get()
        val firstAction = events.first().action
        val uniform = events.all { it.action == firstAction }
        val kind = when {
            uniform && firstAction == "hover_move" -> MoveKind.LATEST_ONLY
            uniform && firstAction == "move" -> MoveKind.MERGE
            else -> MoveKind.BOUNDARY
        }
        val entry = Outbound.Input(generation, events, firstAction, kind)

        val shouldSchedule: Boolean
        synchronized(queueLock) {
            val last = sendQueue.lastOrNull()
            if (kind != MoveKind.BOUNDARY && last is Outbound.Input &&
                last.generation == generation && last.kind == kind && last.action == firstAction
            ) {
                sendQueue[sendQueue.size - 1] =
                    if (kind == MoveKind.MERGE) {
                        val merged = last.events + events
                        Outbound.Input(
                            generation,
                            if (merged.size > MAX_MERGED_SAMPLES) merged.takeLast(MAX_MERGED_SAMPLES) else merged,
                            firstAction,
                            kind
                        )
                    } else {
                        entry
                    }
            } else {
                sendQueue.addLast(entry)
                trimQueueLocked()
            }
            shouldSchedule = !drainScheduled
            if (shouldSchedule) drainScheduled = true
        }
        if (shouldSchedule) ioExecutor.execute { drainQueueOnIoThread() }
    }

    private fun startReaderThread(sock: Socket, generation: Long) {
        Thread({
            try {
                val input = sock.getInputStream()
                while (generation == connectGeneration.get()) {
                    val frame = UsbMuxFrameCodec.read(input)
                    if (frame.streamId != UsbMuxFrameCodec.STREAM_CONTROL) continue
                    handleDownstreamPayload(frame.payload)
                }
            } catch (_: Exception) {
            }
            if (generation == connectGeneration.get() && state.get() == ConnectionState.CONNECTED) {
                ioExecutor.execute {
                    handleIoFailure(Exception("host closed the input channel"), generation)
                }
            }
        }, "input-reader").apply { isDaemon = true }.start()
    }

    private fun handleDownstreamPayload(payload: ByteArray) {
        try {
            val obj = JSONObject(String(payload, Charsets.UTF_8))
            when (obj.optString("type")) {
                "displayMode" -> {
                    val w = obj.optInt("width")
                    val h = obj.optInt("height")
                    if (w > 0 && h > 0) listener.onHostDisplayMode(w, h, obj.optInt("fps"))
                }
                "hostInfo" -> {
                    val id = obj.optString("hostId")
                    if (id.isNotEmpty()) listener.onHostInfo(id)
                }
                "clearDisplayMode" -> listener.onClearHostDisplayModePreference()
                "sessionPolicy" -> listener.onSessionPolicy(
                    obj.optBoolean("keepAliveOnBackground", false),
                    obj.optString("videoScaling", ""),
                    obj.optBoolean("wifiEnabled", false),
                    obj.optString("wifiAuthToken", ""),
                    obj.optBoolean("wifiTlsRequired", false),
                    obj.optString("wifiTlsFingerprint", ""),
                    obj.optBoolean("penPrediction", false)
                )
            }
        } catch (_: Exception) {
        }
    }

    /** Reports the display mode remembered for the current host (zeros = none). */
    fun sendModePrefs(width: Int, height: Int, fps: Int) {
        if (state.get() != ConnectionState.CONNECTED) return
        val payload = JSONObject().apply {
            put("type", "modePrefs")
            put("prefWidth", width)
            put("prefHeight", height)
            put("prefFps", fps)
        }.toString().toByteArray(Charsets.UTF_8)
        enqueueBoundary(connectGeneration.get(), payload, UsbMuxFrameCodec.STREAM_CONTROL)
    }

    fun sendKey(action: String, keycode: Int, meta: Int, chars: String?) {
        if (state.get() != ConnectionState.CONNECTED) return
        val generation = connectGeneration.get()
        val payload = JSONObject().apply {
            put("t", "key")
            put("a", action)
            put("k", keycode)
            put("m", meta)
            put("c", chars ?: "")
            put("ts", System.currentTimeMillis())
        }.toString().toByteArray(Charsets.UTF_8)
        enqueueBoundary(generation, payload, UsbMuxFrameCodec.STREAM_INPUT)
    }

    private fun enqueueBoundary(generation: Long, payload: ByteArray, streamId: Byte) {
        val shouldSchedule: Boolean
        synchronized(queueLock) {
            sendQueue.addLast(Outbound.Raw(generation, listOf(payload), streamId))
            trimQueueLocked()
            shouldSchedule = !drainScheduled
            if (shouldSchedule) drainScheduled = true
        }
        if (shouldSchedule) ioExecutor.execute { drainQueueOnIoThread() }
    }

    // Keep the outbound queue hard-bounded. Prefer dropping stale move
    // batches, then oldest remaining entries if only boundary events remain.
    private fun trimQueueLocked() {
        while (sendQueue.size > MAX_QUEUED_BATCHES) {
            val victim = sendQueue.indexOfFirst { it is Outbound.Input && it.kind != MoveKind.BOUNDARY }
            if (victim >= 0) {
                sendQueue.removeAt(victim)
            } else {
                sendQueue.removeFirstOrNull()
            }
        }
    }

    private fun drainQueueOnIoThread() {
        while (true) {
            val entry: Outbound
            synchronized(queueLock) {
                val next = sendQueue.removeFirstOrNull()
                if (next == null) {
                    drainScheduled = false
                    return
                }
                entry = next
            }
            if (!isGenerationActiveOnIoThread(entry.generation)) continue
            try {
                val payloads = when (entry) {
                    is Outbound.Raw -> entry.payloads
                    is Outbound.Input -> entry.events.map { eventToPayload(it) }
                }
                writePayloadsOnIoThread(entry.streamId, payloads)
            } catch (e: Exception) {
                synchronized(queueLock) { drainScheduled = false }
                handleIoFailure(e, entry.generation)
                return
            }
        }
    }

    private fun handleIoFailure(error: Exception, generation: Long) {
        synchronized(lifecycleLock) {
            if (generation != connectGeneration.get()) return
            if (state.get() == ConnectionState.STOPPED) return
            state.set(ConnectionState.WAITING)
            closeActiveConnectionNow()
        }
        clearSendQueue()
        listener.onWaitingForHost(error.message ?: "unknown")
        scheduleReconnect(generation)
    }

    private fun clearSendQueue() {
        synchronized(queueLock) { sendQueue.clear() }
    }

    private fun buildHelloPayload(): ByteArray {
        val info = displayInfo
        val endpoint = activeEndpoint
        val obj = JSONObject().apply {
            put("type", "hello")
            put("device", "${Build.MANUFACTURER} ${Build.MODEL}")
            put("android", Build.VERSION.RELEASE ?: "unknown")
            put("androidApiLevel", AndroidCompat.sdkInt)
            put("app", appVersion)
            put("clientOS", "android")
            put("clientType", "tablet")
            put("clientVersion", appVersion)
            put("supportsWindowMetrics", AndroidCompat.supportsWindowMetrics)
            put("supportsSurfaceFrameRateHint", AndroidCompat.supportsSurfaceFrameRateHint)
            put("supportsLowLatencyDecoderHint", AndroidCompat.supportsLowLatencyDecoderHint)
            put("screenWidth", info?.nativeWidth ?: 0)
            put("screenHeight", info?.nativeHeight ?: 0)
            info?.refreshRates?.takeIf { it.isNotEmpty() }?.let {
                put("screenRefreshRates", JSONArray(it))
            }
            // Display preferences are kept per host: the host answers with hostInfo and
            // the app replies with modePrefs for that host.
            put("hostPrefs", true)
            put("videoCodecs", advertisedVideoCodecs())
            put("videoFeatures", JSONArray().put("tile1").put("meta1"))
            put("inputProtocol", "mux-v1")
            put("timestamp", System.currentTimeMillis())
            if (!endpoint?.authToken.isNullOrBlank()) {
                put("authToken", endpoint?.authToken)
            }
        }
        return obj.toString().toByteArray(Charsets.UTF_8)
    }

    private fun advertisedVideoCodecs(): JSONArray {
        val codecs = JSONArray().put("h264")
        if (supportsDecode(MediaFormat.MIMETYPE_VIDEO_HEVC)) {
            codecs.put("hevc")
        }
        return codecs
    }

    private fun supportsDecode(mime: String): Boolean {
        return try {
            MediaCodecList(MediaCodecList.REGULAR_CODECS).codecInfos.any { info ->
                !info.isEncoder && info.supportedTypes.any { it.equals(mime, ignoreCase = true) }
            }
        } catch (_: Exception) {
            false
        }
    }

    private fun eventToPayload(event: InputEventPacket): ByteArray {
        val obj = JSONObject().apply {
            put("t", "input")
            put("a", event.action)
            put("x", event.x)
            put("y", event.y)
            put("p", event.pressure)
            put("ti", event.tilt)
            put("o", event.orientation)
            put("b", event.buttons)
            put("tt", event.toolType)
            put("id", event.pointerId)
            put("pc", event.pointerCount)
            put("w", event.width)
            put("h", event.height)
            put("ts", event.timestampMs)
            put("sq", EchoTracker.nextSeq())
        }
        return obj.toString().toByteArray(Charsets.UTF_8)
    }

    private fun writePayloadsOnIoThread(streamId: Byte, payloads: List<ByteArray>) {
        val out = output ?: throw IllegalStateException("output is not connected")
        // One socket write per batch: each frame is otherwise a header write
        // plus a payload write, i.e. separate packets with TCP_NODELAY/TLS.
        val batch = java.io.ByteArrayOutputStream(payloads.sumOf { it.size + UsbMuxFrameCodec.HEADER_SIZE })
        for (payload in payloads) {
            UsbMuxFrameCodec.writeFrame(
                batch,
                UsbMuxFrameCodec.Frame(
                    streamId = streamId,
                    flags = 0,
                    seq = nextTxSeq++,
                    payload = payload
                )
            )
        }
        batch.writeTo(out)
        out.flush()
    }

    private fun dialEndpointHappyEyeballs(
        endpoints: List<HostEndpoint>,
        staggerMs: Long,
        connect: (HostEndpoint) -> Socket
    ): Pair<Socket, HostEndpoint> {
        if (endpoints.isEmpty()) throw IllegalStateException("no endpoints")
        if (endpoints.size == 1) return Pair(connect(endpoints[0]), endpoints[0])

        val executor: ExecutorService = Executors.newFixedThreadPool(endpoints.size)
        val completion = ExecutorCompletionService<Pair<HostEndpoint, Socket>>(executor)
        val winnerChosen = AtomicBoolean(false)
        var lastErr: Exception? = null
        try {
            endpoints.forEachIndexed { idx, endpoint ->
                completion.submit(Callable {
                    if (idx > 0) Thread.sleep(staggerMs * idx)
                    val sock = connect(endpoint)
                    if (winnerChosen.compareAndSet(false, true)) {
                        Pair(endpoint, sock)
                    } else {
                        closeQuietly(null, sock)
                        throw IllegalStateException("dial lost race")
                    }
                })
            }
            repeat(endpoints.size) {
                try {
                    val (endpoint, sock) = completion.take().get()
                    return Pair(sock, endpoint)
                } catch (e: Exception) {
                    val cause = e.cause
                    if (cause is Exception) lastErr = cause else lastErr = e
                }
            }
            throw (lastErr ?: IllegalStateException("no reachable endpoint"))
        } finally {
            executor.shutdownNow()
            executor.awaitTermination(100, TimeUnit.MILLISECONDS)
        }
    }

    private fun isGenerationActiveOnIoThread(generation: Long): Boolean {
        return generation == connectGeneration.get() && state.get() == ConnectionState.CONNECTED
    }

    private fun closeActiveConnectionNow() {
        closeQuietly(output, socket)
        output = null
        socket = null
        activeEndpoint = null
    }

    private fun closeQuietly(out: OutputStream?, s: Socket?) {
        try {
            out?.close()
        } catch (_: Exception) {
        }
        try {
            s?.close()
        } catch (_: Exception) {
        }
    }

    companion object {
        private const val CONNECT_TIMEOUT_MS = 3000
        private const val RECONNECT_DELAY_MS = 1000L
        private const val FALLBACK_RACE_DELAY_MS = 200L
        private const val MAX_QUEUED_BATCHES = 32
        private const val MAX_MERGED_SAMPLES = 512
    }
}
