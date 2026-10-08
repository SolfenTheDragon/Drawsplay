package com.drawsplay.tablet.video

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class DecoderBacklogPolicyTest {

    @Test
    fun trimsToCompleteAvcIdrAndItsParameterSets() {
        val sps = avcNal(7, 0x11)
        val pps = avcNal(8, 0x22)
        val idrStart = avcNal(5, 0x80)
        val idrContinuation = avcNal(5, 0x40)
        val followingPicture = avcNal(1, 0x80)
        val pending = listOf(
            avcNal(1, 0x80),
            avcNal(1, 0x80),
            avcNal(1, 0x80),
            avcNal(1, 0x80),
            sps,
            pps,
            idrStart,
            idrContinuation,
            followingPicture
        )

        val result = DecoderBacklogPolicy.trim(pending, "h264", maxPendingNals = 5)

        assertFalse(result.awaitRandomAccess)
        assertNalListEquals(
            listOf(sps, pps, idrStart, idrContinuation, followingPicture),
            result.retained
        )
    }

    @Test
    fun waitsForKeyframeRatherThanKeepingBrokenPredictionChain() {
        val pending = List(6) { avcNal(1, 0x80) }

        val result = DecoderBacklogPolicy.trim(pending, "h264", maxPendingNals = 4)

        assertTrue(result.awaitRandomAccess)
        assertTrue(result.retained.isEmpty())
    }

    @Test
    fun idrContinuationIsNotTreatedAsAccessUnitStart() {
        val pending = List(5) { avcNal(1, 0x80) } + avcNal(5, 0x40)

        val result = DecoderBacklogPolicy.trim(pending, "h264", maxPendingNals = 4)

        assertTrue(result.awaitRandomAccess)
        assertTrue(result.retained.isEmpty())
    }

    @Test
    fun waitsWhenRecoveredGopStillExceedsBound() {
        val pending = listOf(avcNal(5, 0x80)) + List(6) { avcNal(1, 0x80) }

        val result = DecoderBacklogPolicy.trim(pending, "h264", maxPendingNals = 4)

        assertTrue(result.awaitRandomAccess)
        assertTrue(result.retained.isEmpty())
    }

    @Test
    fun recognizesHevcRandomAccessAndRetainsVpsSpsPps() {
        val vps = hevcNal(32, 0x01)
        val sps = hevcNal(33, 0x02)
        val pps = hevcNal(34, 0x03)
        val idr = hevcNal(19, 0x80)
        val pending = listOf(
            hevcNal(1, 0x80),
            hevcNal(1, 0x80),
            vps,
            sps,
            pps,
            idr
        )

        val result = DecoderBacklogPolicy.trim(pending, "hevc", maxPendingNals = 4)

        assertFalse(result.awaitRandomAccess)
        assertNalListEquals(listOf(vps, sps, pps, idr), result.retained)
        assertTrue(DecoderBacklogPolicy.inspect(idr, "hevc").isRandomAccessStart)
        assertEquals(
            DecoderBacklogPolicy.ParameterSet.VPS,
            DecoderBacklogPolicy.inspect(vps, "hevc").parameterSet
        )
    }

    @Test
    fun marksPictureStartsAndCleanRandomAccess() {
        fun avc(type: Int, first: Int) = DecoderBacklogPolicy.inspect(avcNal(type, first), "h264")
        fun hevc(type: Int, first: Int) = DecoderBacklogPolicy.inspect(hevcNal(type, first), "hevc")

        assertTrue(avc(1, 0x80).startsPicture)
        assertFalse(avc(1, 0x80).isCleanRandomAccessStart)
        assertFalse("later slice of the same picture", avc(1, 0x00).startsPicture)
        assertTrue(avc(2, 0x80).startsPicture)
        assertTrue(avc(5, 0x80).startsPicture)
        assertTrue(avc(5, 0x80).isCleanRandomAccessStart)
        assertFalse(avc(5, 0x00).isCleanRandomAccessStart)
        assertFalse("SEI", avc(6, 0x80).startsPicture)
        assertFalse("SPS", avc(7, 0x80).startsPicture)

        assertTrue(hevc(1, 0x80).startsPicture)
        assertFalse(hevc(1, 0x00).startsPicture)
        assertTrue("IDR_W_RADL", hevc(19, 0x80).isCleanRandomAccessStart)
        assertTrue("BLA_W_LP", hevc(16, 0x80).isCleanRandomAccessStart)
        assertTrue("CRA is a random-access point", hevc(21, 0x80).isRandomAccessStart)
        assertFalse("CRA leading pictures may need older frames", hevc(21, 0x80).isCleanRandomAccessStart)
        assertFalse("VPS", hevc(32, 0x80).startsPicture)
        assertFalse("prefix SEI", hevc(39, 0x80).startsPicture)

        assertTrue(DecoderBacklogPolicy.startsPicture(avcNal(1, 0x80), "h264"))
        assertFalse(DecoderBacklogPolicy.startsPicture(avcNal(8, 0x80), "h264"))
    }

    @Test
    fun skipsBacklogToNewestKeyframeWithLatestParameterSets() {
        val oldSps = avcNal(7, 0x01)
        val oldPps = avcNal(8, 0x01)
        val newSps = avcNal(7, 0x02)
        val newPps = avcNal(8, 0x02)
        val idr = avcNal(5, 0x80)
        val idrSecondSlice = avcNal(5, 0x00)
        val next = avcNal(1, 0x80)
        val pending = listOf(
            avcNal(1, 0x80),
            avcNal(1, 0x80),
            oldSps,
            oldPps,
            avcNal(5, 0x80),
            avcNal(1, 0x80),
            avcNal(1, 0x00),
            newSps,
            newPps,
            idr,
            idrSecondSlice,
            next
        )

        val result = DecoderBacklogPolicy.skipToCleanRandomAccess(pending, "h264")!!

        assertNalListEquals(listOf(newSps, newPps, idr, idrSecondSlice, next), result.retained)
        assertEquals(4, result.droppedPictures)
        assertEquals(2, DecoderBacklogPolicy.countPictureStarts(result.retained, "h264"))
    }

    @Test
    fun keyframeSkipNeedsACleanRandomAccessPicture() {
        val avc = List(4) { avcNal(1, 0x80) }
        assertEquals(null, DecoderBacklogPolicy.skipToCleanRandomAccess(avc, "h264"))

        val hevcCraOnly = listOf(hevcNal(1, 0x80), hevcNal(21, 0x80), hevcNal(1, 0x80))
        assertEquals(null, DecoderBacklogPolicy.skipToCleanRandomAccess(hevcCraOnly, "hevc"))
    }

    private fun avcNal(type: Int, firstPayloadByte: Int): ByteArray =
        byteArrayOf(0, 0, 0, 1, type.toByte(), firstPayloadByte.toByte())

    private fun hevcNal(type: Int, firstPayloadByte: Int): ByteArray =
        byteArrayOf(
            0,
            0,
            0,
            1,
            (type shl 1).toByte(),
            1,
            firstPayloadByte.toByte()
        )

    private fun assertNalListEquals(expected: List<ByteArray>, actual: List<ByteArray>) {
        assertEquals(expected.size, actual.size)
        expected.zip(actual).forEach { (expectedNal, actualNal) ->
            assertArrayEquals(expectedNal, actualNal)
        }
    }
}

