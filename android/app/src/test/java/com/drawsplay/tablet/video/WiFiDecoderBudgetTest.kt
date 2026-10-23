package com.drawsplay.tablet.video

import org.junit.Assert.assertEquals
import org.junit.Test

class WiFiDecoderBudgetTest {
    @Test
    fun ratedThroughputBelowCeilingCapsFps() {
        // OMX.sprd.h264.decoder: 491520 MB/s / (120 x 75 MB) = 54.6 fps at 1920x1200.
        assertEquals(54, WiFiDecoderBudget.maxFps(54.61))
    }

    @Test
    fun fastDecoderKeepsTheCeiling() {
        assertEquals(60, WiFiDecoderBudget.maxFps(240.0))
    }

    @Test
    fun unknownOrImplausibleRatingKeepsTheCeiling() {
        assertEquals(60, WiFiDecoderBudget.maxFps(null))
        assertEquals(60, WiFiDecoderBudget.maxFps(Double.NaN))
        assertEquals(60, WiFiDecoderBudget.maxFps(0.0))
    }

    @Test
    fun verySlowDecoderIsHeldAtTheFloor() {
        assertEquals(24, WiFiDecoderBudget.maxFps(12.0))
    }
}
