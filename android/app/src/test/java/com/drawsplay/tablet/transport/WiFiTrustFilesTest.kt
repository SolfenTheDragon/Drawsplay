package com.drawsplay.tablet.transport

import java.io.File
import java.io.IOException
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

class WiFiTrustFilesTest {
    @get:Rule val temporary = TemporaryFolder()

    @Test
    fun noSavedHostMeansNoForgetActionIsNeeded() {
        assertFalse(WiFiTrustFiles(File(temporary.root, "trust.bin")).hasSavedHost())
    }

    @Test
    fun forgettingDeletesUnreadableTrustAndAtomicRecoveryFiles() {
        val file = File(temporary.root, "trust.bin")
        for (suffix in listOf("", ".bak", ".new")) File(file.path + suffix).writeText("corrupt trust")
        val trust = WiFiTrustFiles(file)
        assertTrue(trust.hasSavedHost())
        trust.revoke()
        assertFalse(trust.hasSavedHost())
        assertFalse(WiFiTrustFiles(file).hasSavedHost())
    }

    @Test
    fun incompleteAtomicWriteStillOffersForget() {
        val file = File(temporary.root, "trust.bin")
        File(file.path + ".new").writeText("partial")
        assertTrue(WiFiTrustFiles(file).hasSavedHost())
    }

    @Test
    fun failedDeletionIsReportedRatherThanClaimingHostWasForgotten() {
        val file = File(temporary.root, "trust.bin")
        assertTrue(file.mkdir())
        File(file, "blocks-deletion").writeText("keep")
        val trust = WiFiTrustFiles(file)
        assertThrows(IOException::class.java) { trust.revoke() }
        assertTrue(trust.hasSavedHost())
    }
}
