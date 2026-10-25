package com.drawsplay.tablet

import com.drawsplay.tablet.transport.WiFiHostResetState
import com.drawsplay.tablet.transport.WiFiSessionCancellation
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.io.InputStream
import org.junit.Assert.*
import org.junit.Test

class WiFiPairingCompletionTest {
    @Test fun confirmedOutcomeSurvivesCancellationAsConfirmationIsRead() {
        val reset = WiFiHostResetState()
        reset.begin()
        reset.finish()
        val epoch = reset.pairingEpoch()!!
        val resources = WiFiSessionCancellation()
        val input = object : InputStream() {
            override fun read(): Int {
                resources.close()
                return 2
            }
        }
        val output = ByteArrayOutputStream()
        WiFiPairingCompletion.confirm(input, output) { reset.paired(epoch) }
        assertArrayEquals(byteArrayOf(1), output.toByteArray())
        assertFalse(reset.reconnectBlocked)
    }

    @Test fun confirmedOldOutcomeCannotUndoAnotherForget() {
        val reset = WiFiHostResetState()
        val epoch = reset.pairingEpoch()!!
        val input = object : InputStream() {
            override fun read(): Int {
                reset.begin()
                reset.finish()
                return 2
            }
        }
        assertThrows(IOException::class.java) {
            WiFiPairingCompletion.confirm(input, ByteArrayOutputStream()) { reset.paired(epoch) }
        }
        assertTrue(reset.reconnectBlocked)
    }

    @Test fun missingHostConfirmationNeverPublishesSuccess() {
        for (confirmation in listOf(byteArrayOf(), byteArrayOf(0), byteArrayOf(1))) {
            var published = false
            assertThrows(IOException::class.java) {
                WiFiPairingCompletion.confirm(ByteArrayInputStream(confirmation), ByteArrayOutputStream()) {
                    published = true
                    true
                }
            }
            assertFalse(published)
        }
    }
}
