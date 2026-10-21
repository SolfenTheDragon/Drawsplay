package com.drawsplay.tablet.video

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class DecoderConfigPlanTest {
    private val hw = listOf("OMX.vendor.h264.decoder", "c2.vendor.avc.decoder")
    private val sw = listOf("c2.android.avc.decoder")

    @Test
    fun firstAttemptIsDefaultHardwareDecoderWithFullTuning() {
        val candidates = DecoderConfigPlan.orderCandidates(hw, sw, skipHardware = false)
        val first = DecoderConfigPlan.attempts(candidates, null).first()
        assertEquals(DecoderCandidate(null, software = false), first.decoder)
        assertEquals(FormatTier.FULL, first.tier)
    }

    @Test
    fun hardwareIsExhaustedBeforeSoftware() {
        val attempts = DecoderConfigPlan.attempts(
            DecoderConfigPlan.orderCandidates(hw, sw, skipHardware = false), null
        )
        val firstSoftware = attempts.indexOfFirst { it.decoder.software }
        assertTrue(attempts.take(firstSoftware).none { it.decoder.software })
        assertTrue(attempts.drop(firstSoftware).all { it.decoder.software })
    }

    @Test
    fun everyDecoderTriesAllTiersFromMostToLeastTuned() {
        val attempts = DecoderConfigPlan.attempts(listOf(DecoderCandidate(null, false)), null)
        assertEquals(FormatTier.values().toList(), attempts.map { it.tier })
    }

    @Test
    fun skipHardwarePutsSoftwareFirst() {
        val candidates = DecoderConfigPlan.orderCandidates(hw, sw, skipHardware = true)
        assertTrue(candidates.first().software)
    }

    @Test
    fun preferredWorkingConfigGoesFirstWithoutDuplicates() {
        val candidates = DecoderConfigPlan.orderCandidates(hw, sw, skipHardware = false)
        val preferred = ConfigAttempt(DecoderCandidate(null, false), FormatTier.BASIC)
        val attempts = DecoderConfigPlan.attempts(candidates, preferred)
        assertEquals(preferred, attempts.first())
        assertEquals(attempts.size, attempts.toSet().size)
    }

    @Test
    fun tiersDropOptionalKeysProgressively() {
        assertTrue(FormatTier.FULL.vendorKeys)
        assertFalse(FormatTier.STANDARD.vendorKeys)
        assertTrue(FormatTier.RATE_HINTS.rateHints)
        assertFalse(FormatTier.RATE_HINTS.lowLatencyHint)
        assertFalse(FormatTier.BASIC.lowLatencyHint)
        assertTrue(FormatTier.BASIC.maxInputSize)
        assertFalse(FormatTier.MINIMAL.maxInputSize)
    }
}
