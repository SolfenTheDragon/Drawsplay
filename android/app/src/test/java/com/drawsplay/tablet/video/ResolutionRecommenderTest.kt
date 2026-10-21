package com.drawsplay.tablet.video

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ResolutionRecommenderTest {
    private val anyRate: (Int, Int) -> Boolean = { _, _ -> true }

    @Test
    fun smallPanelOnLowRamNeedsNoCap() {
        assertNull(ResolutionRecommender.recommend(1200, 800, 2048, false, anyRate))
    }

    @Test
    fun largePanelOnLowRamIsCappedKeepingAspect() {
        val r = ResolutionRecommender.recommend(1920, 1200, 2800, false, anyRate)
        assertNotNull(r)
        assertTrue(r!!.width * r.height <= 1_100_000)
        assertEquals(1280, r.width)
        assertEquals(800, r.height)
    }

    @Test
    fun snapRoundsDownToStandardResolution() {
        assertEquals(1280 to 800, ResolutionRecommender.snapToStandard(1320, 824))
        assertEquals(1920 to 1080, ResolutionRecommender.snapToStandard(1950, 1100))
        assertEquals(500 to 300, ResolutionRecommender.snapToStandard(500, 300))
    }

    @Test
    fun highRamIsNotCapped() {
        assertNull(ResolutionRecommender.recommend(2560, 1600, 8192, false, anyRate))
    }

    @Test
    fun decoderLimitStepsDown() {
        val r = ResolutionRecommender.recommend(2560, 1600, 8192, false) { w, _ -> w <= 1920 }
        assertNotNull(r)
        assertTrue(r!!.width <= 1920)
    }

    @Test
    fun unknownNativeSizeYieldsNothing() {
        assertNull(ResolutionRecommender.recommend(0, 0, 2048, true, anyRate))
    }
}
