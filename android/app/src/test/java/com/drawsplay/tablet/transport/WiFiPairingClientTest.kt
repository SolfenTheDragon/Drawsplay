package com.drawsplay.tablet.transport

import java.math.BigInteger
import java.security.MessageDigest
import java.security.SecureRandom
import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec
import org.bouncycastle.crypto.agreement.srp.SRP6Server
import org.bouncycastle.crypto.agreement.srp.SRP6StandardGroups
import org.bouncycastle.crypto.agreement.srp.SRP6VerifierGenerator
import org.bouncycastle.crypto.digests.SHA256Digest
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

class WiFiPairingClientTest {
    private val host = ByteArray(32) { 1 }
    private val tablet = ByteArray(32) { 2 }
    private val salt = ByteArray(16) { 3 }
    private val nonce = ByteArray(32) { 4 }
    private val group = SRP6StandardGroups.rfc5054_3072

    private fun server(code: String): SRP6Server {
        val verifier = SRP6VerifierGenerator()
        verifier.init(group, SHA256Digest())
        val value = verifier.generateVerifier(salt, "DrawSplay-WiFi-v2".toByteArray(), code.toByteArray())
        return SRP6Server().apply { init(group, value, SHA256Digest(), SecureRandom()) }
    }

    private fun padded(value: BigInteger): ByteArray {
        val encoded = value.toByteArray()
        val result = ByteArray(384)
        val start = if (encoded.size == 385) 1 else 0
        encoded.copyInto(result, result.size - (encoded.size - start), start)
        return result
    }

    @Test
    fun verifiesHostOnlyAfterBothIdentitiesAndClientFirstProofAreBound() {
        val server = server("01234567")
        val publicB = padded(server.generateServerCredentials())
        val client = WiFiPairingClient("01234567", host, tablet, salt, nonce, publicB)
        val publicA = client.publicValue()
        val secret = server.calculateSecret(BigInteger(1, publicA))
        // Upstream Go SRP derives K from lowercase, unpadded ASCII hex(S).
        val key = MessageDigest.getInstance("SHA-256").digest(secret.toString(16).toByteArray(Charsets.US_ASCII))
        val transcript = "DrawSplay numeric pairing\u0000".toByteArray() +
            byteArrayOf(0, 0, 0, 2) + host + tablet + nonce + salt + publicA + publicB
        fun proof(role: String): ByteArray = Mac.getInstance("HmacSHA256").run {
            init(SecretKeySpec(key, "HmacSHA256"))
            doFinal(role.toByteArray() + transcript)
        }
        assertArrayEquals(proof("tablet confirms\u0000"), client.proof())
        assertTrue(client.verifyHost(proof("host confirms\u0000")))
        try {
            client.verifyHost(proof("host confirms\u0000"))
            fail("host proof replay accepted")
        } catch (expected: java.io.IOException) {
        }
    }

    @Test
    fun wrongCodeAndChangedIdentityDoNotConfirmTheHost() {
        for (mode in listOf("code", "identity", "bad-proof")) {
            val server = server("01234567")
            val publicB = padded(server.generateServerCredentials())
            val client = WiFiPairingClient(
                if (mode == "code") "12345678" else "01234567",
                host, tablet, salt, nonce, publicB
            )
            val publicA = client.publicValue()
            val secret = server.calculateSecret(BigInteger(1, publicA))
            val key = MessageDigest.getInstance("SHA-256").digest(secret.toString(16).toByteArray(Charsets.US_ASCII))
            val otherHost = host.copyOf().apply { if (mode == "identity") this[0] = 9 }
            val transcript = "DrawSplay numeric pairing\u0000".toByteArray() +
                byteArrayOf(0, 0, 0, 2) + otherHost + tablet + nonce + salt + publicA + publicB
            val proof = Mac.getInstance("HmacSHA256").run {
                init(SecretKeySpec(key, "HmacSHA256"))
                doFinal("host confirms\u0000".toByteArray() + transcript)
            }
            if (mode == "bad-proof") proof[0] = (proof[0].toInt() xor 1).toByte()
            client.proof()
            assertFalse(client.verifyHost(proof))
        }
    }

    @Test
    fun rejectsMalformedChallengesBeforeExpensiveGroupOperations() {
        val publicB = padded(server("01234567").generateServerCredentials())
        val invalid = listOf(
            { WiFiPairingClient("1234567x", host, tablet, salt, nonce, publicB) },
            { WiFiPairingClient("12345678", ByteArray(32), tablet, salt, nonce, publicB) },
            { WiFiPairingClient("12345678", host, tablet, ByteArray(15), nonce, publicB) },
            { WiFiPairingClient("12345678", host, tablet, salt, ByteArray(31), publicB) },
            { WiFiPairingClient("12345678", host, tablet, salt, nonce, ByteArray(385)) },
            { WiFiPairingClient("12345678", host, tablet, salt, nonce, ByteArray(384)) },
            { WiFiPairingClient("12345678", host, tablet, salt, nonce, padded(BigInteger.ONE)) },
            { WiFiPairingClient("12345678", host, tablet, salt, nonce, padded(group.n)) }
        )
        for (create in invalid) {
            try {
                create()
                fail("invalid pairing parameters accepted")
            } catch (expected: java.io.IOException) {
            }
        }
    }
}
