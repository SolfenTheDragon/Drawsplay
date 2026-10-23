package com.drawsplay.tablet.video

import com.drawsplay.tablet.transport.WiFiSessionProtocol
import java.io.IOException
import java.nio.ByteBuffer
import org.junit.Assert.*
import org.junit.Test

class WiFiH264Test {
    // FFmpeg/libx264 constrained-baseline, progressive 640x360, level 3.1.
    private val sps = hex("000000016742c01fda0280bfe5c044000003000400000300f23c60ca80")
    private val pps = hex("0000000168ce0fc8")
    private fun limits() = WiFiSessionProtocol.Hello(
        2, 2, 2, 1, 1280, 720, 30, 31, 262144, 524288, 2, 1, 1,
        ByteArray(32) { 1 }, ByteArray(32) { 2 }
    )

    private fun config(width: Int = 640, height: Int = 360, fps: Int = 30, sps: ByteArray = this.sps): ByteArray =
        ByteBuffer.allocate(16 + sps.size + pps.size).apply {
            putShort(1).putShort(width.toShort()).putShort(height.toShort()).putShort(fps.toShort())
            putShort(sps.size.toShort()).putShort(pps.size.toShort()).putInt(0)
            put(sps).put(pps)
        }.array()

    private class BitWriter {
        private val out = java.io.ByteArrayOutputStream()
        private var cur = 0
        private var n = 0
        fun bit(b: Int) = apply {
            cur = (cur shl 1) or b
            if (++n == 8) { out.write(cur); cur = 0; n = 0 }
        }
        fun bits(value: Int, count: Int) = apply { for (i in count - 1 downTo 0) bit((value ushr i) and 1) }
        fun ue(value: Int) = apply {
            val v = value + 1
            val len = 32 - Integer.numberOfLeadingZeros(v)
            bits(0, len - 1); bits(v, len)
        }
        fun finish(): ByteArray { bit(1); while (n != 0) bit(0); return out.toByteArray() }
    }

    // Baseline SPS with poc type 2 and frame_num 4 bits; no emulation-prevention bytes are needed here.
    private fun spsFor(level: Int, refs: Int, wMb: Int, hMb: Int, cropBottomUnits: Int = 0): ByteArray {
        val w = BitWriter().bits(66, 8).bits(0xc0, 8).bits(level, 8)
            .ue(0).ue(0).ue(2).ue(refs).bit(0).ue(wMb - 1).ue(hMb - 1).bit(1).bit(1)
        if (cropBottomUnits > 0) w.bit(1).ue(0).ue(0).ue(0).ue(cropBottomUnits) else w.bit(0)
        w.bit(0)
        return hex("00000001" + "67") + w.finish()
    }

    @Test
    fun configRejectsExcessiveCodedSizeAndDpbOverflowForLevel() {
        // 2048x368 coded cropped to 640x360 must not pass although visible size fits the maxima.
        val wide = spsFor(31, 1, 128, 23, 4).also { }
        assertThrows(IOException::class.java) { WiFiH264.decodeConfig(config(sps = wide), limits()) }
        // 1280x720 level 3.1 permits at most 18000/3600 = 5 reference frames.
        assertThrows(IOException::class.java) {
            WiFiH264.decodeConfig(config(1280, 720, 15, spsFor(31, 16, 80, 45)), limits().copy(maxWidth = 1280))
        }
        // Control: the same size with a legal reference count and bounded padding is accepted.
        WiFiH264.decodeConfig(config(1280, 720, 15, spsFor(31, 5, 80, 45)), limits())
        WiFiH264.decodeConfig(config(sps = spsFor(31, 1, 40, 23, 4)), limits())
    }

    @Test
    fun configChecksActualSpsDimensionsProfileLevelAndNegotiatedLimits() {
        val config = WiFiH264.decodeConfig(config(), limits())
        assertEquals(640, config.width)
        assertEquals(360, config.height)
        assertEquals(30, config.fps)
        assertArrayEquals(sps, config.sps)
        for (raw in listOf(
            config(1280, 720), config(640, 360, 60),
            config().also { it[20 + 1] = 100 }, // SPS profile_idc.
            config().also { it[20 + 3] = 42 }, // SPS exceeds negotiated level.
            config().also { it[15] = 1 },
            config().copyOf(17),
            config().also { it[9] = 0x7f }
        )) {
            assertThrows(IOException::class.java) { WiFiH264.decodeConfig(raw, limits()) }
        }
        assertThrows(IOException::class.java) {
            WiFiH264.decodeConfig(config(), limits().copy(maxWidth = 320))
        }
        val embedded = config()
        val hiddenSps = sps + hex("00000167640033")
        val smuggled = ByteBuffer.allocate(16 + hiddenSps.size + pps.size).apply {
            put(embedded, 0, 16)
            putShort(8, hiddenSps.size.toShort())
            put(hiddenSps).put(pps)
        }.array()
        assertThrows(IOException::class.java) { WiFiH264.decodeConfig(smuggled, limits()) }
    }

    private fun slice(header: Int, firstMb: Int, sliceType: Int, frameNum: Int, idrId: Int = 0): ByteArray {
        val w = BitWriter().ue(firstMb).ue(sliceType).ue(0).bits(frameNum, 4)
        if (header and 31 == 5) w.ue(idrId)
        return hex("00000001" + "%02x".format(header)) + w.finish()
    }

    private fun aud(primaryPicType: Int) = hex("00000001" + "09" + "%02x".format(primaryPicType shl 5 or 16))

    @Test
    fun completeAudDelimitedPicturesIdentifyIdrWithoutCopyingNalPayloads() {
        val scanner = WiFiH264(WiFiH264.decodeConfig(config(), limits()))
        val idr = aud(0) + slice(0x65, 0, 7, 0) + slice(0x65, 1, 7, 0)
        assertTrue(scanner.inspect(idr, idr.size))
        val predicted = aud(1) + slice(0x41, 0, 5, 1) + slice(0x41, 1, 5, 1)
        assertFalse(scanner.inspect(predicted, predicted.size))
        repeat(10000) { assertTrue(scanner.inspect(idr, idr.size)) }
    }

    @Test
    fun slicesBelongingToDifferentPicturesInOneAccessUnitAreRejected() {
        val scanner = WiFiH264(WiFiH264.decodeConfig(config(), limits()))
        val head = aud(1)
        for (raw in listOf(
            head + slice(0x41, 0, 5, 1) + slice(0x41, 1, 5, 2), // frame_num differs.
            head + slice(0x41, 0, 5, 1) + slice(0x01, 1, 5, 1), // nal_ref_idc zero vs nonzero.
            aud(0) + slice(0x65, 0, 7, 0, 1) + slice(0x65, 1, 7, 0, 2) // idr_pic_id differs.
        )) {
            assertThrows(IOException::class.java) { scanner.inspect(raw, raw.size) }
        }
    }

    @Test
    fun malformedFragmentedMultiPictureOrReconfiguredAccessUnitsAreRejected() {
        val scanner = WiFiH264(WiFiH264.decodeConfig(config(), limits()))
        for (raw in listOf(
            hex("00000165b8"), // Missing AUD.
            hex("0000010910"), // No picture.
            hex("000001091000000165"), // Truncated slice header.
            hex("000001091000000165b8000001091000000165b8"), // Two pictures.
            hex("000001091000000165b800000165b8"), // Two first slices.
            hex("000001091000000165b800000141e0"), // Mixed IDR/non-IDR.
            hex("00000109100000016742"), // In-band parameter change.
            hex("0000010910000001e5b8"), // Forbidden header bit.
            hex("000001093000000141a0"), // B slice.
            hex("000001091000000165b4") // Unconfigured PPS 1.
        )) {
            assertThrows(IOException::class.java) { scanner.inspect(raw, raw.size) }
        }
    }

    private fun hex(value: String): ByteArray = value.chunked(2).map { it.toInt(16).toByte() }.toByteArray()
}
