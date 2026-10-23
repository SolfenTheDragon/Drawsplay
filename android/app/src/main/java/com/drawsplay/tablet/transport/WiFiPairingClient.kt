package com.drawsplay.tablet.transport

import java.io.Closeable
import java.io.IOException
import java.math.BigInteger
import java.nio.ByteBuffer
import java.security.MessageDigest
import java.security.SecureRandom
import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec
import org.bouncycastle.crypto.CryptoException
import org.bouncycastle.crypto.agreement.srp.SRP6Client
import org.bouncycastle.crypto.agreement.srp.SRP6StandardGroups
import org.bouncycastle.crypto.digests.SHA256Digest

/** Single-owner enrollment only. Never run the expensive group operations on the UI thread. */
class WiFiPairingClient(
    code: String,
    hostSpkiSha256: ByteArray,
    tabletSpkiSha256: ByteArray,
    salt: ByteArray,
    nonce: ByteArray,
    hostPublicValue: ByteArray
) : Closeable {
    private val publicA: ByteArray
    private val tabletProof: ByteArray
    private val hostProof: ByteArray
    private var proofSent = false
    private var finished = false

    init {
        if (code.length != 8 || code.any { it !in '0'..'9' } ||
            hostSpkiSha256.size != 32 || hostSpkiSha256.all { it == 0.toByte() } ||
            tabletSpkiSha256.size != 32 || tabletSpkiSha256.all { it == 0.toByte() } ||
            salt.size != 16 || nonce.size != 32 || hostPublicValue.size != 384
        ) throw IOException(FAILURE)
        val group = SRP6StandardGroups.rfc5054_3072
        val publicB = BigInteger(1, hostPublicValue)
        if (publicB <= BigInteger.ONE || publicB >= group.n) throw IOException(FAILURE)
        val client = SRP6Client()
        client.init(group, SHA256Digest(), SecureRandom())
        val password = code.toByteArray(Charsets.US_ASCII)
        val valueA = try {
            client.generateClientCredentials(salt, "DrawSplay-WiFi-v2".toByteArray(Charsets.US_ASCII), password)
        } finally {
            password.fill(0)
        }
        val encodedA = valueA.toByteArray()
        val start = if (encodedA.size == 385) 1 else 0
        publicA = ByteArray(384)
        encodedA.copyInto(publicA, publicA.size - (encodedA.size - start), start)
        val secret = try {
            client.calculateSecret(publicB)
        } catch (e: CryptoException) {
            throw IOException(FAILURE, e)
        }
        // Matches pinned 1Password/srp Key(), not Bouncy Castle's padded H(S).
        val encodedSecret = secret.toString(16).toByteArray(Charsets.US_ASCII)
        val key = try {
            MessageDigest.getInstance("SHA-256").digest(encodedSecret)
        } finally {
            encodedSecret.fill(0)
        }
        val domain = "DrawSplay numeric pairing\u0000".toByteArray(Charsets.US_ASCII)
        val transcript = ByteBuffer.allocate(domain.size + 4 + 32 + 32 + 32 + 16 + 384 + 384)
            .put(domain).putInt(2).put(hostSpkiSha256).put(tabletSpkiSha256)
            .put(nonce).put(salt).put(publicA).put(hostPublicValue).array()
        try {
            tabletProof = confirmation(key, "tablet confirms\u0000", transcript)
            hostProof = confirmation(key, "host confirms\u0000", transcript)
        } finally {
            key.fill(0)
        }
    }

    fun publicValue(): ByteArray {
        if (finished) throw IOException(FAILURE)
        return publicA.copyOf()
    }

    fun proof(): ByteArray {
        if (finished || proofSent) throw IOException(FAILURE)
        proofSent = true
        return tabletProof.copyOf()
    }

    fun verifyHost(proof: ByteArray): Boolean {
        if (finished || !proofSent) throw IOException(FAILURE)
        return try {
            proof.size == 32 && MessageDigest.isEqual(hostProof, proof)
        } finally {
            close()
        }
    }

    override fun close() {
        finished = true
        tabletProof.fill(0)
        hostProof.fill(0)
    }

    private fun confirmation(key: ByteArray, role: String, transcript: ByteArray): ByteArray =
        Mac.getInstance("HmacSHA256").run {
            init(SecretKeySpec(key, "HmacSHA256"))
            update(role.toByteArray(Charsets.US_ASCII))
            doFinal(transcript)
        }

    companion object {
        private const val FAILURE = "Wi-Fi pairing failed; reopen pairing locally and verify the code"
    }
}
