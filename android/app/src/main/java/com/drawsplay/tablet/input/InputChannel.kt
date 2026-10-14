package com.drawsplay.tablet.input

import android.os.Build
import android.media.MediaCodecList
import android.media.MediaFormat
import com.drawsplay.tablet.AndroidCompat
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
        fun onHostCursor(x: Float, y: Float, visible: Boolean)
        fun onHostDisplayMode(width: Int, height: Int)
        fun onClearHostDisplayModePreference()
        fun onSessionPolicy(
            keepAliveOnBackground: Boolean,
            videoScaling: String,
            wifiEnabled: Boolean,
            wifiAuthToken: String,
            wifiTlsRequired: Boolean,
            wifiTlsFingerprint: String
        )
    }

    data class DisplayInfo(
        val nativeWidth: Int,
        val nativeHeight: Int,
        val prefWidth: Int,
        val prefHeight: Int
    )

    @Volatile
    var displayInfo: DisplayInfo? = null

    private enum class ConnectionState { STOPPED, WAITING, CONNECTED }

    private sealed class Outbound {
        abstract val generation: Long
        abstract val payloads: List<ByteArray>
        abstract val streamId: Byte

        class Boundary(
            override val generation: Long,
            override val payloads: List<ByteArray>,
            override val streamId: Byte
        ) : Outbound()

        class Moves(
            override val generation: Long,
            override val payloads: List<ByteArray>,
            val action: String,
            override val streamId: Byte
        ) : Outbound()
    }

    private val ioExecutor = Executors.newSingleThreadScheduledExecutor()
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
        val payloads = events.map { eventToPayload(it) }
        val firstAction = events.first().action
        val coalescible = (firstAction == "move" || firstAction == "hover_move") &&
            events.all { it.action == firstAction }
        val entry: Outbound =
            if (coalescible) {
                Outbound.Moves(generation, payloads, firstAction, UsbMuxFrameCodec.STREAM_INPUT)
            } else {
                Outbound.Boundary(generation, payloads, UsbMuxFrameCodec.STREAM_INPUT)
            }

        val shouldSchedule: Boolean
        synchronized(queueLock) {
            val last = sendQueue.lastOrNull()
            if (entry is Outbound.Moves && last is Outbound.Moves &&
                last.generation == generation && last.action == entry.action
            ) {
                sendQueue[sendQueue.size - 1] = entry
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
                    if (w > 0 && h > 0) listener.onHostDisplayMode(w, h)
                }
                "clearDisplayMode" -> listener.onClearHostDisplayModePreference()
                "sessionPolicy" -> listener.onSessionPolicy(
                    obj.optBoolean("keepAliveOnBackground", false),
                    obj.optString("videoScaling", ""),
                    obj.optBoolean("wifiEnabled", false),
                    obj.optString("wifiAuthToken", ""),
                    obj.optBoolean("wifiTlsRequired", false),
                    obj.optString("wifiTlsFingerprint", "")
                )
                "cursor" -> listener.onHostCursor(
                    obj.optDouble("x", 0.0).toFloat(),
                    obj.optDouble("y", 0.0).toFloat(),
                    obj.optBoolean("visible", false)
                )
            }
        } catch (_: Exception) {
        }
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
            sendQueue.addLast(Outbound.Boundary(generation, listOf(payload), streamId))
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
            val victim = sendQueue.indexOfFirst { it is Outbound.Moves }
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
                writePayloadsOnIoThread(entry.streamId, entry.payloads)
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
            put("prefWidth", info?.prefWidth ?: 0)
            put("prefHeight", info?.prefHeight ?: 0)
            put("videoCodecs", advertisedVideoCodecs())
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
        }
        return obj.toString().toByteArray(Charsets.UTF_8)
    }

    private fun writePayloadsOnIoThread(streamId: Byte, payloads: List<ByteArray>) {
        val out = output ?: throw IllegalStateException("output is not connected")
        for (payload in payloads) {
            UsbMuxFrameCodec.writeFrame(
                out,
                UsbMuxFrameCodec.Frame(
                    streamId = streamId,
                    flags = 0,
                    seq = nextTxSeq++,
                    payload = payload
                )
            )
        }
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
    }
}
