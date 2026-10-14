package com.drawsplay.tablet.video

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class VideoScalingTest {

    @Test
    fun fillAlwaysCoversThePanel() {
        assertNull(VideoScaling.FILL.frameSize(1920, 1080, 2960, 1848))
    }

    @Test
    fun unknownVideoSizeCoversThePanel() {
        assertNull(VideoScaling.FIT.frameSize(0, 0, 2960, 1848))
        assertNull(VideoScaling.ORIGINAL.frameSize(1920, 1080, 0, 0))
    }

    @Test
    fun fitKeepsAspectRatio() {
        // 16:9 onto a 16:10 panel: full width, bars top and bottom.
        assertEquals(2960 to 1665, VideoScaling.FIT.frameSize(1920, 1080, 2960, 1848))
        // 4:3 onto 16:10: full height, bars left and right.
        assertEquals(2464 to 1848, VideoScaling.FIT.frameSize(1024, 768, 2960, 1848))
    }

    @Test
    fun originalIsPixelForPixelWhenItFits() {
        assertEquals(1920 to 1080, VideoScaling.ORIGINAL.frameSize(1920, 1080, 2960, 1848))
    }

    @Test
    fun originalShrinksLikeFitWhenTooLarge() {
        assertEquals(
            VideoScaling.FIT.frameSize(3840, 2160, 2960, 1848),
            VideoScaling.ORIGINAL.frameSize(3840, 2160, 2960, 1848)
        )
    }

    @Test
    fun parsesWireNames() {
        assertEquals(VideoScaling.FIT, VideoScaling.fromWireName("fit"))
        assertEquals(VideoScaling.ORIGINAL, VideoScaling.fromWireName(" Original "))
        assertEquals(VideoScaling.FILL, VideoScaling.fromWireName("fill"))
        assertNull(VideoScaling.fromWireName(""))
        assertNull(VideoScaling.fromWireName("stretch"))
    }
}
