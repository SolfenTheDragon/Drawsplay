package com.drawsplay.tablet.transport

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyInfo
import android.security.keystore.KeyProperties
import android.util.AtomicFile
import java.io.File
import java.io.FileNotFoundException
import java.io.IOException
import java.math.BigInteger
import java.net.Socket
import java.security.GeneralSecurityException
import java.security.KeyPairGenerator
import java.security.KeyFactory
import java.security.KeyStore
import java.security.MessageDigest
import java.security.Principal
import java.security.PrivateKey
import java.security.SecureRandom
import java.security.cert.X509Certificate
import java.security.spec.ECGenParameterSpec
import java.util.Date
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.net.ssl.KeyManager
import javax.net.ssl.X509KeyManager
import javax.security.auth.x500.X500Principal

/** Wi-Fi opt-in only. Single-owner; never falls back to plaintext preferences. */
class WiFiTrustStore(context: Context) {
    private val directory = File(context.applicationContext.noBackupFilesDir, "wifi-v2")
    private val file = File(directory, "trust.bin")
    private val atomic = AtomicFile(file)
    private val keys = keystoreOperation {
        KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
    }
    private val identity by lazy { loadIdentity() }

    fun identitySpkiSha256(): ByteArray =
        MessageDigest.getInstance("SHA-256").digest(identity.certificate.publicKey.encoded)

    fun keyManagers(): Array<KeyManager> {
        val entry = identity
        val chain = entry.certificateChain.map {
            it as? X509Certificate ?: throw IOException("Keystore returned a non-X509 Wi-Fi identity")
        }.toTypedArray()
        return arrayOf(object : X509KeyManager {
            override fun chooseClientAlias(keyType: Array<out String>?, issuers: Array<out Principal>?, socket: Socket?): String? =
                if (keyType?.contains("EC") == true) IDENTITY_ALIAS else null
            override fun getClientAliases(keyType: String?, issuers: Array<out Principal>?): Array<String>? =
                if (keyType == "EC") arrayOf(IDENTITY_ALIAS) else null
            override fun chooseServerAlias(keyType: String?, issuers: Array<out Principal>?, socket: Socket?): String? = null
            override fun getServerAliases(keyType: String?, issuers: Array<out Principal>?): Array<String>? = null
            override fun getCertificateChain(alias: String?): Array<X509Certificate>? =
                if (alias == IDENTITY_ALIAS) chain.copyOf() else null
            override fun getPrivateKey(alias: String?): PrivateKey? =
                if (alias == IDENTITY_ALIAS) entry.privateKey else null
        })
    }

    fun readPairedHost(): WiFiPairedHost? {
        val input = try {
            atomic.openRead()
        } catch (e: FileNotFoundException) {
            if (hasStoredTrust()) throw IOException("Wi-Fi trust cannot be read; repair locally", e)
            return null
        }
        val buffer = ByteArray(WiFiTrustRecordCodec.MAX_RECORD_BYTES + 1)
        val count = input.use {
            var used = 0
            while (used < buffer.size) {
                val read = it.read(buffer, used, buffer.size - used)
                if (read == -1) break
                if (read == 0) throw IOException("Wi-Fi trust file read made no progress")
                used += read
            }
            used
        }
        if (count > WiFiTrustRecordCodec.MAX_RECORD_BYTES) throw IOException("Wi-Fi trust file is oversized; repair locally")
        return WiFiTrustRecordCodec.open(trustKey(create = false), buffer.copyOf(count))
    }

    fun savePairedHost(pin: ByteArray, label: String) {
        // Never persist a peer that cannot be associated with our own identity.
        identity
        // An existing record with a missing Keystore key must not be overwritten
        // until the user explicitly forgets that broken trust.
        val encoded = WiFiTrustRecordCodec.seal(trustKey(create = !hasStoredTrust()), pin, label)
        if (!directory.isDirectory && !directory.mkdirs()) throw IOException("Cannot create private Wi-Fi trust directory")
        val output = atomic.startWrite()
        try {
            output.write(encoded)
            output.fd.sync()
            atomic.finishWrite(output)
        } catch (e: IOException) {
            atomic.failWrite(output)
            throw IOException("Cannot persist encrypted Wi-Fi trust", e)
        }
        val stored = readPairedHost()
        if (stored == null || !MessageDigest.isEqual(stored.spkiSha256, pin) || stored.label != label) {
            throw IOException("Wi-Fi trust replacement did not persist; pairing was not saved")
        }
    }

    /** Callers must also close sessions and cancel reconnect before forgetting. */
    fun revoke() {
        atomic.delete()
        if (hasStoredTrust() || File(file.path + ".new").exists()) {
            throw IOException("Cannot remove Wi-Fi trust; stop Wi-Fi and retry")
        }
    }

    private fun loadIdentity(): KeyStore.PrivateKeyEntry = keystoreOperation {
        if (!keys.containsAlias(IDENTITY_ALIAS)) {
            if (hasStoredTrust()) throw IOException("Wi-Fi identity key is missing; explicitly forget and re-pair locally")
            val now = System.currentTimeMillis()
            KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, "AndroidKeyStore").apply {
                initialize(
                    KeyGenParameterSpec.Builder(IDENTITY_ALIAS, KeyProperties.PURPOSE_SIGN or KeyProperties.PURPOSE_VERIFY)
                        .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
                        // Conscrypt signs its already-hashed TLS transcript with NONEwithECDSA.
                        .setDigests(KeyProperties.DIGEST_NONE, KeyProperties.DIGEST_SHA256, KeyProperties.DIGEST_SHA384)
                        .setUserAuthenticationRequired(false)
                        .setCertificateSubject(X500Principal("CN=DrawSplay LAN tablet"))
                        .setCertificateSerialNumber(BigInteger(128, SecureRandom()).add(BigInteger.ONE))
                        .setCertificateNotBefore(Date(now - 3600000))
                        .setCertificateNotAfter(Date(now + 5L * 365 * 24 * 3600000))
                        .build()
                )
                generateKeyPair()
            }
        }
        val entry = keys.getEntry(IDENTITY_ALIAS, null) as? KeyStore.PrivateKeyEntry
            ?: throw IOException("Wi-Fi identity is unavailable; repair Keystore and re-pair locally")
        if (entry.privateKey.encoded != null) throw IOException("Wi-Fi identity key is unexpectedly exportable")
        val information = KeyFactory.getInstance(KeyProperties.KEY_ALGORITHM_EC, "AndroidKeyStore")
            .getKeySpec(entry.privateKey, KeyInfo::class.java)
        if (KeyProperties.DIGEST_NONE !in information.digests) {
            throw IOException("Existing Wi-Fi identity cannot sign TLS transcripts; explicitly reset Wi-Fi identity and re-pair locally")
        }
        (entry.certificate as? X509Certificate)?.checkValidity()
            ?: throw IOException("Wi-Fi identity certificate is unavailable")
        entry
    }

    private fun trustKey(create: Boolean): SecretKey = keystoreOperation {
        if (!keys.containsAlias(TRUST_ALIAS)) {
            if (!create) throw IOException("Wi-Fi trust encryption key is missing; repair and re-pair locally")
            KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").apply {
                init(
                    KeyGenParameterSpec.Builder(TRUST_ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                        .setKeySize(256)
                        .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                        .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                        .setRandomizedEncryptionRequired(true)
                        .setUserAuthenticationRequired(false)
                        .build()
                )
                generateKey()
            }
        }
        val key = keys.getKey(TRUST_ALIAS, null) as? SecretKey
            ?: throw IOException("Wi-Fi trust encryption key is unavailable")
        if (key.encoded != null) throw IOException("Wi-Fi trust key is unexpectedly exportable")
        key
    }

    private fun hasStoredTrust(): Boolean = file.exists() || File(file.path + ".bak").exists()

    private inline fun <T> keystoreOperation(action: () -> T): T =
        try {
            action()
        } catch (e: GeneralSecurityException) {
            throw IOException("Android Keystore cannot protect Wi-Fi identity; no plaintext fallback", e)
        }

    companion object {
        private const val IDENTITY_ALIAS = "DrawSplayWiFiIdentityV1"
        private const val TRUST_ALIAS = "DrawSplayWiFiTrustV1"
    }
}
