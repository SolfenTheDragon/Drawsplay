package com.drawsplay.tablet.transport

import java.io.IOException
import java.nio.ByteBuffer
import java.security.MessageDigest

/** Connection-time messages only. A valid hello is not a substitute for pinned mutual TLS. */
object WiFiSessionProtocol {
    const val TABLET = 1
    const val HOST = 2
    const val FEATURE_LOW_LATENCY = 1L
    /** Tablet sends decode statistics (control subtype 8) upstream on the video socket. */
    const val FEATURE_VIDEO_STATS = 2L
    const val HELLO_SIZE = 96

    data class Hello(
        val minMajor: Int,
        val maxMajor: Int,
        val role: Int,
        val codecMask: Int,
        val maxWidth: Int,
        val maxHeight: Int,
        val maxFps: Int,
        val h264Level: Int,
        val maxAccessUnit: Int,
        val queueBytes: Int,
        val queueSlots: Int,
        val statsVersion: Int,
        val features: Long,
        val spki: ByteArray,
        val nonce: ByteArray
    )

    fun decodeHello(raw: ByteArray, role: Int, authenticatedSpki: ByteArray): Hello {
        if (raw.size != HELLO_SIZE || raw[5] != 0.toByte() || role !in TABLET..HOST || authenticatedSpki.size != 32) {
            throw IOException(INCOMPATIBLE)
        }
        val bytes = ByteBuffer.wrap(raw)
        fun u16(): Int = bytes.short.toInt() and 65535
        val min = u16()
        val max = u16()
        val actualRole = bytes.get().toInt() and 255
        bytes.get()
        val hello = Hello(
            min, max, actualRole, u16(), u16(), u16(), u16(), u16(),
            bytes.int, bytes.int, u16(), u16(), bytes.int.toLong() and 0xffffffffL,
            ByteArray(32).also { bytes.get(it) }, ByteArray(32).also { bytes.get(it) }
        )
        validate(hello)
        if (hello.role != role || !MessageDigest.isEqual(hello.spki, authenticatedSpki)) throw IOException(INCOMPATIBLE)
        return hello
    }

    fun encodeHello(hello: Hello, destination: ByteArray) {
        if (destination.size != HELLO_SIZE) throw IOException(INCOMPATIBLE)
        validate(hello)
        ByteBuffer.wrap(destination).apply {
            putShort(hello.minMajor.toShort()).putShort(hello.maxMajor.toShort())
            put(hello.role.toByte()).put(0).putShort(hello.codecMask.toShort())
            putShort(hello.maxWidth.toShort()).putShort(hello.maxHeight.toShort())
            putShort(hello.maxFps.toShort()).putShort(hello.h264Level.toShort())
            putInt(hello.maxAccessUnit).putInt(hello.queueBytes)
            putShort(hello.queueSlots.toShort()).putShort(hello.statsVersion.toShort())
            putInt(hello.features.toInt()).put(hello.spki).put(hello.nonce)
        }
    }

    fun negotiate(host: Hello, tablet: Hello): Hello {
        validate(host)
        validate(tablet)
        if (host.role != HOST || tablet.role != TABLET) throw IOException(INCOMPATIBLE)
        return host.copy(
            minMajor = 2, maxMajor = 2, codecMask = 1,
            maxWidth = minOf(host.maxWidth, tablet.maxWidth),
            maxHeight = minOf(host.maxHeight, tablet.maxHeight),
            maxFps = minOf(host.maxFps, tablet.maxFps),
            h264Level = minOf(host.h264Level, tablet.h264Level),
            maxAccessUnit = minOf(host.maxAccessUnit, tablet.maxAccessUnit),
            queueBytes = minOf(host.queueBytes, tablet.queueBytes),
            queueSlots = minOf(host.queueSlots, tablet.queueSlots),
            features = host.features and tablet.features and (FEATURE_LOW_LATENCY or FEATURE_VIDEO_STATS)
        ).also { validate(it) }
    }

    /** Secret ticket for a single video channel; never log or persist it. */
    data class Offer(val id: ByteArray, val epoch: Long, val ticket: ByteArray)

    fun decodeOffer(raw: ByteArray, epoch: Long): Offer {
        if (raw.size != 56 || raw[0] != 1.toByte() || raw[1] != 0.toByte() ||
            raw[2] != 0.toByte() || raw[3] != 1.toByte() || ByteBuffer.wrap(raw).getInt(52) != 5000
        ) throw IOException("Invalid Wi-Fi session offer; reconnect control first")
        return Offer(raw.copyOfRange(4, 20), epoch, raw.copyOfRange(20, 52)).also { validateOffer(it) }
    }

    fun encodeAttach(offer: Offer, destination: ByteArray) {
        if (destination.size != 52) throw IOException("Invalid Wi-Fi attachment buffer")
        validateOffer(offer)
        destination.fill(0)
        destination[0] = 2
        destination[3] = 1
        offer.id.copyInto(destination, 4)
        offer.ticket.copyInto(destination, 20)
    }

    private fun validateOffer(offer: Offer) {
        if (offer.epoch == 0L || offer.id.size != 16 || offer.id.all { it == 0.toByte() } ||
            offer.ticket.size != 32 || offer.ticket.all { it == 0.toByte() }
        ) throw IOException("Missing Wi-Fi session generation or video credential")
    }

    private fun validate(hello: Hello) {
        if (hello.minMajor !in 1..2 || hello.maxMajor !in 2..65535 || hello.minMajor > hello.maxMajor ||
            hello.role !in TABLET..HOST || hello.codecMask !in 1..65535 || hello.codecMask and 1 == 0 ||
            hello.maxWidth !in 320..8192 || hello.maxHeight !in 320..8192 || hello.maxFps !in 1..120 ||
            hello.h264Level !in LEVELS || hello.maxAccessUnit !in 65536..WiFiFrameCodec.MAX_ACCESS_UNIT ||
            hello.queueSlots !in 2..4 || hello.queueBytes !in 0..8 * 1024 * 1024 ||
            hello.maxAccessUnit.toLong() * hello.queueSlots > hello.queueBytes ||
            hello.statsVersion != 1 || hello.features !in 0..0xffffffffL ||
            hello.spki.size != 32 || hello.spki.all { it == 0.toByte() } ||
            hello.nonce.size != 32 || hello.nonce.all { it == 0.toByte() }
        ) throw IOException(INCOMPATIBLE)
    }

    private val LEVELS = setOf(31, 32, 40, 41, 42, 50, 51, 52)
    private const val INCOMPATIBLE = "Incompatible Wi-Fi capabilities, identity or memory limits; update both endpoints or lower the mode"
}
