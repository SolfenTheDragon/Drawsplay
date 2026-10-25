package com.drawsplay.tablet.transport

import java.io.Closeable
import java.io.IOException

/** Owns sockets from listener creation through handshake and streaming. */
internal class WiFiSessionCancellation : Closeable {
    private val resources = mutableSetOf<Closeable>()
    @Volatile var isActive = true
        private set

    @Synchronized
    fun <T : Closeable> track(resource: T): T {
        if (!isActive) {
            resource.close()
            throw IOException("Wi-Fi session cancelled")
        }
        resources.add(resource)
        return resource
    }

    @Synchronized
    fun publish(action: () -> Unit): Boolean {
        if (!isActive) return false
        action()
        return true
    }

    @Synchronized
    fun release(resource: Closeable) {
        if (resources.remove(resource)) resource.close()
    }

    @Synchronized
    override fun close() {
        isActive = false
        var failure: IOException? = null
        for (resource in resources) {
            try {
                resource.close()
            } catch (error: IOException) {
                if (failure == null) failure = error else failure.addSuppressed(error)
            }
        }
        resources.clear()
        failure?.let { throw it }
    }
}
