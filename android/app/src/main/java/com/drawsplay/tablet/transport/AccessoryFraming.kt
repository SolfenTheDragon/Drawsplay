package com.drawsplay.tablet.transport

/**
 * Link framing for the USB accessory (AOA) transport. Mirrors host/aoa_link.go:
 * 5-byte big-endian header (type, channel id, payload length) followed by the payload.
 */
object AccessoryFraming {
    const val HEADER_BYTES = 5
    const val MAX_PAYLOAD = 16000

    const val TYPE_HELLO = 1
    const val TYPE_OPEN = 2
    const val TYPE_DATA = 3
    const val TYPE_CLOSE = 4

    const val KIND_INPUT = 1
    const val KIND_VIDEO = 2

    const val LINK_VERSION = 1
    val HELLO_PAYLOAD = byteArrayOf('D'.code.toByte(), 'S'.code.toByte(), 'A'.code.toByte(), 'O'.code.toByte(), 'A'.code.toByte(), LINK_VERSION.toByte())

    fun isValidHello(payload: ByteArray): Boolean =
        payload.size >= HELLO_PAYLOAD.size &&
            HELLO_PAYLOAD.copyOfRange(0, 5).contentEquals(payload.copyOfRange(0, 5)) &&
            payload[5].toInt() == LINK_VERSION

    fun encode(type: Int, id: Int, payload: ByteArray, offset: Int = 0, length: Int = payload.size): ByteArray {
        require(length in 0..MAX_PAYLOAD) { "payload too large: $length" }
        val out = ByteArray(HEADER_BYTES + length)
        out[0] = type.toByte()
        out[1] = (id ushr 8).toByte()
        out[2] = id.toByte()
        out[3] = (length ushr 8).toByte()
        out[4] = length.toByte()
        System.arraycopy(payload, offset, out, HEADER_BYTES, length)
        return out
    }
}

/** Reassembles frames from an arbitrary split of the accessory byte stream. */
class AccessoryFrameParser(private val onFrame: (type: Int, id: Int, payload: ByteArray) -> Unit) {
    private val buf = ByteArray(AccessoryFraming.HEADER_BYTES + AccessoryFraming.MAX_PAYLOAD)
    private var have = 0

    fun feed(data: ByteArray, length: Int) {
        var pos = 0
        while (pos < length) {
            val want = if (have < AccessoryFraming.HEADER_BYTES) {
                AccessoryFraming.HEADER_BYTES - have
            } else {
                AccessoryFraming.HEADER_BYTES + payloadLength() - have
            }
            val n = minOf(want, length - pos)
            System.arraycopy(data, pos, buf, have, n)
            have += n
            pos += n
            if (have < AccessoryFraming.HEADER_BYTES) continue
            val len = payloadLength()
            if (len > AccessoryFraming.MAX_PAYLOAD) throw IllegalStateException("bad accessory frame length $len")
            if (have == AccessoryFraming.HEADER_BYTES + len) {
                val type = buf[0].toInt() and 0xFF
                val id = ((buf[1].toInt() and 0xFF) shl 8) or (buf[2].toInt() and 0xFF)
                val payload = buf.copyOfRange(AccessoryFraming.HEADER_BYTES, have)
                have = 0
                onFrame(type, id, payload)
            }
        }
    }

    private fun payloadLength(): Int = ((buf[3].toInt() and 0xFF) shl 8) or (buf[4].toInt() and 0xFF)
}
