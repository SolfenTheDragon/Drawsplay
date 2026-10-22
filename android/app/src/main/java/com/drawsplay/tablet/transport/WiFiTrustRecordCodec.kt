package com.drawsplay.tablet.transport

import java.io.IOException
import java.security.GeneralSecurityException
import javax.crypto.Cipher
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

data class WiFiPairedHost(val spkiSha256: ByteArray, val label: String)

/** Only enrollment/storage work; no per-frame code calls this codec. */
object WiFiTrustRecordCodec {
    const val MAX_RECORD_BYTES = 125
    private val DOMAIN = "DrawSplay Wi-Fi trust v1".toByteArray(Charsets.US_ASCII)

    fun seal(key: SecretKey, pin: ByteArray, label: String): ByteArray {
        val name = label.toByteArray(Charsets.UTF_8)
        if (pin.size != 32 || pin.all { it == 0.toByte() } || !validLabel(label, name)) {
            throw IOException("Wi-Fi trust requires a nonzero SPKI pin and a printable 1..63-byte host name")
        }
        val plaintext = ByteArray(33 + name.size)
        plaintext[0] = name.size.toByte()
        pin.copyInto(plaintext, 1)
        name.copyInto(plaintext, 33)
        return try {
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.ENCRYPT_MODE, key)
            if (cipher.iv.size != 12) throw IOException("Keystore provider did not supply a 96-bit GCM nonce")
            cipher.updateAAD(DOMAIN)
            byteArrayOf(1) + cipher.iv + cipher.doFinal(plaintext)
        } catch (e: GeneralSecurityException) {
            throw IOException("Cannot encrypt Wi-Fi trust with Keystore; no plaintext fallback", e)
        } finally {
            plaintext.fill(0)
        }
    }

    fun open(key: SecretKey, encoded: ByteArray): WiFiPairedHost {
        if (encoded.size !in 63..MAX_RECORD_BYTES || encoded[0] != 1.toByte()) {
            throw IOException("Invalid Wi-Fi trust format or size; re-pair locally")
        }
        val plaintext = try {
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(128, encoded, 1, 12))
            cipher.updateAAD(DOMAIN)
            cipher.doFinal(encoded, 13, encoded.size - 13)
        } catch (e: GeneralSecurityException) {
            throw IOException("Cannot authenticate Wi-Fi trust; repair or re-pair locally", e)
        }
        try {
            if (plaintext.size !in 34..96 || plaintext[0].toInt() != plaintext.size - 33) {
                throw IOException("Invalid decrypted Wi-Fi trust length")
            }
            val name = plaintext.copyOfRange(33, plaintext.size)
            val label = name.toString(Charsets.UTF_8)
            val pin = plaintext.copyOfRange(1, 33)
            if (pin.all { it == 0.toByte() } || !validLabel(label, name) ||
                !name.contentEquals(label.toByteArray(Charsets.UTF_8))
            ) throw IOException("Invalid decrypted Wi-Fi identity or label")
            return WiFiPairedHost(pin, label)
        } finally {
            plaintext.fill(0)
        }
    }

    private fun validLabel(label: String, bytes: ByteArray): Boolean =
        bytes.size in 1..63 && bytes.toString(Charsets.UTF_8) == label && label.none { it.isISOControl() }
}
