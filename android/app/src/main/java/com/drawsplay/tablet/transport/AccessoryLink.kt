package com.drawsplay.tablet.transport

import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.net.Socket
import java.net.SocketTimeoutException
import java.util.ArrayDeque
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Multiplexes virtual connections over one USB accessory byte stream.
 * All blocking I/O runs on the link's own threads and on the callers' channel threads,
 * never on the main thread.
 */
class AccessoryLink(
    private val input: InputStream,
    private val output: OutputStream,
    private val onClosed: (AccessoryLink) -> Unit
) {
    private val writeLock = Any()
    private val sockets = ConcurrentHashMap<Int, AccessorySocket>()
    private val ready = CountDownLatch(1)
    private val closed = AtomicBoolean(false)
    private var nextId = 0

    @Volatile
    var isReady = false
        private set

    fun start() {
        Thread({ readerLoop() }, "accessory-reader").apply { isDaemon = true }.start()
        Thread({ helloLoop() }, "accessory-hello").apply { isDaemon = true }.start()
    }

    fun awaitReady(timeoutMs: Long): Boolean = ready.await(timeoutMs, TimeUnit.MILLISECONDS) && !closed.get()

    /** Opens a virtual connection of [kind] (AccessoryFraming.KIND_*). */
    fun open(kind: Int, timeoutMs: Long): AccessorySocket {
        if (!awaitReady(timeoutMs)) throw IOException("USB accessory link not ready")
        val socket: AccessorySocket
        synchronized(this) {
            var id: Int
            do {
                nextId = if (nextId >= 0xFFFF) 1 else nextId + 1
                id = nextId
            } while (sockets.containsKey(id))
            socket = AccessorySocket(this, id)
            sockets[id] = socket
        }
        try {
            writeFrame(AccessoryFraming.TYPE_OPEN, socket.id, byteArrayOf(kind.toByte()))
        } catch (e: IOException) {
            sockets.remove(socket.id)
            throw e
        }
        return socket
    }

    internal fun writeData(id: Int, data: ByteArray, offset: Int, length: Int) {
        var pos = offset
        val end = offset + length
        while (pos < end) {
            val n = minOf(AccessoryFraming.MAX_PAYLOAD, end - pos)
            writeFrame(AccessoryFraming.TYPE_DATA, id, data, pos, n)
            pos += n
        }
    }

    internal fun writeFrame(type: Int, id: Int, payload: ByteArray, offset: Int = 0, length: Int = payload.size) {
        if (closed.get()) throw IOException("USB accessory link closed")
        val frame = AccessoryFraming.encode(type, id, payload, offset, length)
        try {
            synchronized(writeLock) {
                output.write(frame)
                output.flush()
            }
        } catch (e: IOException) {
            close()
            throw e
        }
    }

    internal fun release(id: Int, notifyPeer: Boolean) {
        val removed = sockets.remove(id)
        if (removed != null && notifyPeer && !closed.get()) {
            try {
                writeFrame(AccessoryFraming.TYPE_CLOSE, id, ByteArray(0))
            } catch (_: IOException) {
            }
        }
    }

    fun close() {
        if (!closed.compareAndSet(false, true)) return
        ready.countDown()
        failAll()
        try { input.close() } catch (_: IOException) {}
        try { output.close() } catch (_: IOException) {}
        onClosed(this)
    }

    private fun failAll() {
        val snapshot = sockets.values.toList()
        sockets.clear()
        snapshot.forEach { it.markRemoteClosed() }
    }

    private fun readerLoop() {
        val parser = AccessoryFrameParser { type, id, payload -> dispatch(type, id, payload) }
        val buf = ByteArray(16384)
        try {
            while (!closed.get()) {
                val n = input.read(buf)
                if (n < 0) break
                parser.feed(buf, n)
            }
        } catch (_: IOException) {
        } catch (_: IllegalStateException) {
        }
        close()
    }

    private fun dispatch(type: Int, id: Int, payload: ByteArray) {
        when (type) {
            AccessoryFraming.TYPE_HELLO -> {
                if (!AccessoryFraming.isValidHello(payload)) throw IllegalStateException("bad host hello")
                if (isReady) {
                    // A second hello means the host process restarted and lost every virtual connection.
                    failAll()
                } else {
                    isReady = true
                    ready.countDown()
                }
            }
            AccessoryFraming.TYPE_DATA -> {
                val target = sockets[id]
                if (target != null && !target.offer(payload)) {
                    release(id, true)
                    target.markRemoteClosed()
                }
            }
            AccessoryFraming.TYPE_CLOSE -> sockets.remove(id)?.markRemoteClosed()
        }
    }

    /**
     * The tablet speaks first, and keeps greeting so a restarted host process can
     * complete its handshake without the cable being replugged.
     */
    private fun helloLoop() {
        try {
            while (!closed.get()) {
                writeFrame(AccessoryFraming.TYPE_HELLO, 0, AccessoryFraming.HELLO_PAYLOAD)
                Thread.sleep(HELLO_INTERVAL_MS)
            }
        } catch (_: IOException) {
        } catch (_: InterruptedException) {
        }
    }

    companion object {
        private const val HELLO_INTERVAL_MS = 3000L
    }
}

/** A [Socket] facade over one virtual accessory connection, so existing channels can use it unchanged. */
class AccessorySocket internal constructor(
    private val link: AccessoryLink,
    internal val id: Int
) : Socket() {
    private val lock = Object()
    private val queue = ArrayDeque<ByteArray>()
    private var queuedBytes = 0
    private var current: ByteArray? = null
    private var currentPos = 0
    private var remoteClosed = false
    private val localClosed = AtomicBoolean(false)

    @Volatile
    private var readTimeoutMs = 0

    private val inStream = object : InputStream() {
        override fun read(): Int {
            val one = ByteArray(1)
            return if (read(one, 0, 1) < 0) -1 else one[0].toInt() and 0xFF
        }

        override fun read(b: ByteArray, off: Int, len: Int): Int {
            if (len == 0) return 0
            synchronized(lock) { return readLocked(b, off, len) }
        }

        private fun readLocked(b: ByteArray, off: Int, len: Int): Int {
            val deadline = if (readTimeoutMs > 0) System.nanoTime() + readTimeoutMs * 1_000_000L else 0L
            while (true) {
                if (localClosed.get()) throw IOException("socket closed")
                if (current == null) current = queue.poll()?.also { currentPos = 0 }
                val cur = current
                if (cur != null) {
                    val n = minOf(len, cur.size - currentPos)
                    System.arraycopy(cur, currentPos, b, off, n)
                    currentPos += n
                    queuedBytes -= n
                    if (currentPos >= cur.size) current = null
                    return n
                }
                if (remoteClosed) return -1
                if (deadline == 0L) {
                    lock.wait()
                } else {
                    val leftMs = (deadline - System.nanoTime()) / 1_000_000L
                    if (leftMs <= 0) throw SocketTimeoutException("read timed out")
                    lock.wait(leftMs)
                }
            }
        }

        override fun available(): Int = synchronized(lock) { queuedBytes }

        override fun close() = this@AccessorySocket.close()
    }

    private val outStream = object : OutputStream() {
        override fun write(b: Int) = write(byteArrayOf(b.toByte()), 0, 1)

        override fun write(b: ByteArray, off: Int, len: Int) {
            if (localClosed.get() || synchronized(lock) { remoteClosed }) throw IOException("socket closed")
            link.writeData(id, b, off, len)
        }

        override fun close() = this@AccessorySocket.close()
    }

    /** Returns false when the receiver is too far behind; the caller drops the connection. */
    internal fun offer(data: ByteArray): Boolean = synchronized(lock) {
        if (remoteClosed || localClosed.get()) return true
        if (queuedBytes + data.size > MAX_QUEUED_BYTES) return false
        queue.add(data)
        queuedBytes += data.size
        lock.notifyAll()
        true
    }

    internal fun markRemoteClosed() {
        synchronized(lock) {
            remoteClosed = true
            lock.notifyAll()
        }
    }

    override fun getInputStream(): InputStream = inStream
    override fun getOutputStream(): OutputStream = outStream

    override fun close() {
        if (!localClosed.compareAndSet(false, true)) return
        synchronized(lock) { lock.notifyAll() }
        link.release(id, notifyPeer = true)
    }

    override fun isClosed(): Boolean = localClosed.get()
    override fun isConnected(): Boolean = true
    override fun isInputShutdown(): Boolean = localClosed.get()
    override fun isOutputShutdown(): Boolean = localClosed.get()
    override fun shutdownInput() {}
    override fun shutdownOutput() {}

    override fun setSoTimeout(timeout: Int) { readTimeoutMs = timeout }
    override fun getSoTimeout(): Int = readTimeoutMs
    override fun setTcpNoDelay(on: Boolean) {}
    override fun getTcpNoDelay(): Boolean = true
    override fun setKeepAlive(on: Boolean) {}
    override fun getKeepAlive(): Boolean = false
    override fun setSendBufferSize(size: Int) {}
    override fun getSendBufferSize(): Int = AccessoryFraming.MAX_PAYLOAD
    override fun setReceiveBufferSize(size: Int) {}
    override fun getReceiveBufferSize(): Int = MAX_QUEUED_BYTES

    companion object {
        private const val MAX_QUEUED_BYTES = 8 * 1024 * 1024
    }
}
