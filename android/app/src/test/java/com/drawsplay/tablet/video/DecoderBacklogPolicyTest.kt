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

