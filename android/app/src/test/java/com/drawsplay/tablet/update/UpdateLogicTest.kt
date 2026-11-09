package com.drawsplay.tablet.update

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class UpdateLogicTest {
    @Test
    fun parsesOnlyReleaseVersions() {
        assertEquals(ReleaseVersion(1, 4, 0), ReleaseVersion.parse("v1.4.0"))
        assertEquals(ReleaseVersion(0, 2, 10), ReleaseVersion.parse("0.2.10"))
        for (bad in listOf("dev", "0.2.0-wifi-test", "v1.2", "1.02.3", "", null)) {
            assertNull(bad, ReleaseVersion.parse(bad))
        }
    }

    @Test
    fun comparesNumerically() {
        assertTrue(ReleaseVersion.parse("v1.10.0")!! > ReleaseVersion.parse("v1.9.9")!!)
        assertTrue(ReleaseVersion.parse("v2.0.0")!! > ReleaseVersion.parse("v1.99.99")!!)
        assertEquals(0, ReleaseVersion(1, 2, 3).compareTo(ReleaseVersion(1, 2, 3)))
    }

    @Test
    fun readsSha256Sums() {
        val a = "ab".repeat(32)
        val b = "CD".repeat(32)
        val sums = parseSha256Sums("$a  DrawSplay.APK\n$b *DrawSplay-linux-v1.0.0.tar.gz\nnot a line\n")
        assertEquals(a, sums["DrawSplay.APK"])
        assertEquals(b.lowercase(), sums["DrawSplay-linux-v1.0.0.tar.gz"])
        assertEquals(2, sums.size)
    }

    @Test
    fun downloadsOnlyFromGitHubOverHttps() {
        assertTrue(updateUrlAllowed("https", "github.com"))
        assertTrue(updateUrlAllowed("https", "objects.githubusercontent.com"))
        assertFalse(updateUrlAllowed("http", "github.com"))
        assertFalse(updateUrlAllowed("https", "github.com.evil.test"))
        assertFalse(updateUrlAllowed("https", "example.com"))
    }

    @Test
    fun signerRules() {
        assertTrue(signersCompatible(setOf("k1"), setOf("k1"), multipleSigners = false))
        // Key rotation: the new APK's lineage includes the installed key.
        assertTrue(signersCompatible(setOf("k1"), setOf("k1", "k2"), multipleSigners = false))
        assertFalse(signersCompatible(setOf("k1"), setOf("k2"), multipleSigners = false))
        assertFalse(signersCompatible(setOf("k1", "k2"), setOf("k1"), multipleSigners = true))
        assertFalse(signersCompatible(emptySet(), setOf("k1"), multipleSigners = false))
    }
}
