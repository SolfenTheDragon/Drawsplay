package com.drawsplay.tablet.video

import android.util.Log
import android.view.Surface
import com.drawsplay.tablet.transport.HostEndpoint
import com.drawsplay.tablet.transport.HostEndpointSelector
import com.drawsplay.tablet.transport.TlsSocketFactory
import org.json.JSONObject
import java.io.DataInputStream
import java.io.EOFException
import java.net.InetSocketAddress
import java.net.Socket
import java.util.concurrent.Callable
import java.util.concurrent.ExecutorCompletionService
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicLong
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Video channel client: receives the host's H.264 stream over TCP port 27184
 * (USB via `adb reverse`) and renders it through [VideoDecoder].
 *
 * Wire format (host -> client, big-endian; see host protocol.go):
 *
 *     [1 byte packet type][4 byte payload length][payload]
 *
 *     type 0 (config) â€” JSON {"codec","encoder","fps"}, once per session.
 *     type 1 (nal)    â€” one H.264 Annex-B NAL unit, start code included.
 *
 * Connection model: fully automatic, mirroring the input channel. [start]
 * launches a reader thread that connects, streams until failure, then sleeps
 * [RECONNECT_DELAY_MS] and reconnects â€” forever, until [stop]. An open app is
 * a willing secondary monitor; there is no manual connect control.
 *
 * A fresh [VideoDecoder] is created per connection (the host restarts its
 * encoder per session, so parameter sets always precede pictures).
 */
class VideoChannel(
    private val listener: VideoChannelListener,
    private val endpointSelector: HostEndpointSelector
) {

    /** Owner-facing status callbacks. Invoked on the reader thread. */
    interface VideoChannelListener {
        /** Stream is up; config describes the incoming encode. */
        fun onStreamStarted(encoder: String, fps: Int)

        /** Stream down or not yet up; the channel keeps retrying on its own. */
        fun onWaitingForHost(reason: String?)
    }

    private val generation = AtomicLong(0L)

    @Volatile
    private var surface: Surface? = null

    @Volatile
    private var readerThread: Thread? = null

    // The connection currently being pumped; closed by stop() because
    // Thread.interrupt() cannot unblock a socket readFully.
    @Volatile
    private var activeSocket: Socket? = null

    /**
     * Starts (or restarts) streaming onto [renderSurface]. Called whenever
     * the SurfaceView's surface is (re)created.
     */
    fun start(renderSurface: Surface) {
        stop()
        surface = renderSurface
        val gen = generation.incrementAndGet()
        val thread = Thread({ runReaderLoop(gen) }, "video-channel")
        thread.isDaemon = true
        readerThread = thread
        thread.start()
    }

    /** Stops streaming and joins the reader thread. Safe to call repeatedly. */
    fun stop() {
        generation.incrementAndGet()
        val thread = readerThread ?: return
        readerThread = null
        thread.interrupt()
        // interrupt() cannot wake a blocking socket read; closing the socket
        // can, which guarantees the join below succeeds before the caller
        // destroys the surface the decoder is still rendering to.
        try {
            activeSocket?.close()
        } catch (_: Exception) {
        }
        try {
            thread.join(1000)
        } catch (_: InterruptedException) {
            Thread.currentThread().interrupt()
        }
    }

    /** Reconnect loop: one connection per iteration, retry after any failure. */
    private fun runReaderLoop(gen: Long) {
        listener.onWaitingForHost(null)
        while (gen == generation.get() && !Thread.currentThread().isInterrupted) {
            try {
                streamOneConnection(gen)
            } catch (e: InterruptedException) {
                return
            } catch (e: Exception) {
                if (gen != generation.get()) return
                Log.d(TAG, "Video connection ended: ${e.message}")
                listener.onWaitingForHost(e.message)
            }
            try {
                Thread.sleep(RECONNECT_DELAY_MS)
            } catch (_: InterruptedException) {
                return
            }
        }
    }

    /** Connects once and pumps packets into a fresh decoder until failure. */
    private fun streamOneConnection(gen: Long) {
        val renderSurface = surface ?: throw IllegalStateException("no surface")
        val endpoints = endpointSelector.videoCandidates()
        val (socket, selectedEndpoint) = dialEndpointHappyEyeballs(endpoints, FALLBACK_RACE_DELAY_MS) { endpoint ->
            if (endpoint.tls && endpoint.tlsFingerprintSha256.isNotBlank()) {
                TlsSocketFactory.connectPinned(
                    endpoint.host,
                    endpoint.videoPort,
                    CONNECT_TIMEOUT_MS,
                    endpoint.tlsFingerprintSha256,
                    receiveBufferSize = 1024 * 1024
                )
            } else {
                Socket().apply {
                    receiveBufferSize = 1024 * 1024
                    connect(InetSocketAddress(endpoint.host, endpoint.videoPort), CONNECT_TIMEOUT_MS)
                }
            }.apply {
                tcpNoDelay = true
            }
        }
        val selectedAuthToken = selectedEndpoint.authToken
        socket.use {
            activeSocket = socket
            val auth = JSONObject().apply {
                put("type", "auth")
                put("authToken", selectedAuthToken)
            }.toString() + "\n"
            socket.getOutputStream().write(auth.toByteArray(Charsets.UTF_8))
            socket.getOutputStream().flush()

            val input = DataInputStream(socket.getInputStream().buffered(256 * 1024))
            val decoder = VideoDecoder(renderSurface)
            val statsThread = startStatsReporter(socket, decoder)
            try {
                while (gen == generation.get() && !Thread.currentThread().isInterrupted) {
                    val type = input.readUnsignedByte()
                    val length = input.readInt()
                    if (length < 0 || length > MAX_PACKET_BYTES) {
                        throw IllegalStateException("bad packet length $length")
                    }
                    val payload = ByteArray(length)
                    input.readFully(payload)

                    when (type) {
                        PACKET_CONFIG -> handleConfig(payload, decoder)
                        PACKET_NAL -> decoder.feedNal(payload)
                        else -> Log.w(TAG, "Unknown video packet type $type")
                    }
                }
            } catch (e: EOFException) {
                throw IllegalStateException("host closed the stream")
            } finally {
                activeSocket = null
                statsThread.interrupt()
                decoder.release()
                // Note: never lockCanvas() this surface to blank it â€” that
                // permanently connects it as a CPU producer, and MediaCodec
                // can no longer attach on reconnect ("already connected
                // (cur=2 req=3)"). The activity's opaque status overlay is
                // what hides stale frames after a disconnect.
            }
        }
    }

    /**
     * Starts the upstream statistics reporter (M4): every [STATS_INTERVAL_MS]
     * a JSON line with per-interval decode statistics is written back on the
     * video socket. The host uses these for adaptive bitrate decisions and as
     * a fast dead-peer signal. Reporter failures end silently â€” the reader
     * side will notice a dead socket on its own.
     */
    private fun startStatsReporter(socket: Socket, decoder: VideoDecoder): Thread {
        val output = socket.getOutputStream()
        val thread = Thread({
            try {
                while (!Thread.currentThread().isInterrupted) {
                    Thread.sleep(STATS_INTERVAL_MS)
                    val s = decoder.snapshotStats()
                    val line = JSONObject().apply {
                        put("type", "stats")
                        put("intervalMs", STATS_INTERVAL_MS)
                        put("framesReceived", s.framesReceived)
                        put("framesRendered", s.framesRendered)
                        put("avgDecodeMs", s.avgDecodeMs)
                        put("avgPresentMs", s.avgPresentMs)
                        put("frameDrops", s.frameDrops)
                        put("queuedFrames", s.queuedFrames)
                        put("refreshHz", decoder.displayRefreshHz())
                        put("renderMode", decoder.renderMode)
                        put("pacingGuardrail", decoder.pacingGuardrailApplied)
                    }.toString() + "\n"
                    output.write(line.toByteArray(Charsets.UTF_8))
                    output.flush()
                }
            } catch (_: InterruptedException) {
            } catch (e: Exception) {
                Log.d(TAG, "Stats reporter stopped: ${e.message}")
            }
        }, "video-stats")
        thread.isDaemon = true
        thread.start()
        return thread
    }

    private fun handleConfig(payload: ByteArray, decoder: VideoDecoder) {
        val obj = JSONObject(String(payload, Charsets.UTF_8))
        val codec = obj.optString("codec", "h264").lowercase().let {
            if (it == "h265") "hevc" else it
        }
        if (codec != "h264" && codec != "hevc") {
            throw IllegalStateException("unsupported negotiated video codec $codec")
        }
        val encoder = obj.optString("encoder", "unknown")
        val fps = obj.optInt("fps", 0)
        val pacing = obj.optString("pacing", "latency").lowercase()
        val refreshHz = decoder.displayRefreshHz()
        val selectedPacing = if (pacing == "smooth" && refreshHz in 1f..75f && fps > 45) "latency" else pacing
        val pacingGuardrail = selectedPacing != pacing
        Log.i(TAG, "Stream config: codec=$codec encoder=$encoder fps=$fps")
        decoder.streamCodec = codec
        decoder.renderMode = selectedPacing
        decoder.pacingGuardrailApplied = pacingGuardrail
        decoder.streamFps = fps
        listener.onStreamStarted(encoder, fps)
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
                        try {
                            sock.close()
                        } catch (_: Exception) {
                        }
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

    companion object {
        private const val TAG = "VideoChannel"
        private const val CONNECT_TIMEOUT_MS = 3000
        private const val RECONNECT_DELAY_MS = 1000L
        private const val FALLBACK_RACE_DELAY_MS = 200L
        private const val STATS_INTERVAL_MS = 250L
        private const val PACKET_CONFIG = 0
        private const val PACKET_NAL = 1
        private const val MAX_PACKET_BYTES = 8 * 1024 * 1024
    }
}

