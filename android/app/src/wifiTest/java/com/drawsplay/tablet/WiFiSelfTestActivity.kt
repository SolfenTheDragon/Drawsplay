package com.drawsplay.tablet

import android.app.Activity
import android.os.Build
import android.os.Bundle
import android.util.Log
import android.widget.TextView
import com.drawsplay.tablet.transport.WiFiTrustStore
import java.io.File
import java.io.IOException
import java.security.KeyStore
import java.security.MessageDigest
import java.security.PrivateKey
import java.security.Signature
import java.util.concurrent.Executors

/** Separate application ID, no USB intent handlers, synthetic identities only. */
class WiFiSelfTestActivity : Activity() {
    override fun onCreate(state: Bundle?) {
        super.onCreate(state)
        setContentView(TextView(this).apply { text = "Running isolated Wi-Fi trust tests..." })
        val worker = Executors.newSingleThreadExecutor()
        worker.execute {
            try {
                val store = WiFiTrustStore(applicationContext)
                check(store.readPairedHost() == null) { "Requires a fresh isolated test installation" }
                val pin = ByteArray(32) { 7 }
                store.savePairedHost(pin, "Synthetic host")
                val identity = store.identitySpkiSha256()
                val reopened = WiFiTrustStore(applicationContext)
                check(reopened.identitySpkiSha256().contentEquals(identity))
                check(reopened.readPairedHost()?.spkiSha256?.contentEquals(pin) == true)
                val record = File(noBackupFilesDir, "wifi-v2/trust.bin")
                val original = record.readBytes()
                val changed = original.copyOf().apply { this[22] = (this[22].toInt() xor 1).toByte() }
                record.writeBytes(changed)
                var rejected = false
                try {
                    reopened.readPairedHost()
                } catch (expected: IOException) {
                    rejected = true
                }
                check(rejected) { "Modified trust was accepted or silently discarded" }
                check(reopened.identitySpkiSha256().contentEquals(identity))
                record.writeBytes(original)
                check(reopened.readPairedHost()?.label == "Synthetic host")
                reopened.revoke()
                check(reopened.readPairedHost() == null)
                val keys = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
                check(keys.getKey("DrawSplayWiFiIdentityV1", null).encoded == null)
                check(keys.getKey("DrawSplayWiFiTrustV1", null).encoded == null)
                val transcript = MessageDigest.getInstance("SHA-256").digest("Synthetic TLS transcript".toByteArray(Charsets.US_ASCII))
                val signed = Signature.getInstance("NONEwithECDSA").run {
                    initSign(keys.getKey("DrawSplayWiFiIdentityV1", null) as PrivateKey)
                    update(transcript)
                    sign()
                }
                check(Signature.getInstance("NONEwithECDSA").run {
                    initVerify(keys.getCertificate("DrawSplayWiFiIdentityV1"))
                    update(transcript)
                    verify(signed)
                }) { "Keystore identity cannot sign Conscrypt's prehashed TLS transcript" }
                Log.i("DS-WiFiSelfTest", """{"api":${Build.VERSION.SDK_INT},"trustPersistence":true,"corruptionRejected":true,"nonExportableKeys":true,"revocationPersisted":true,"tlsPrehashedSigning":true}""")
                runOnUiThread { finish() }
            } catch (error: Exception) {
                Log.e("DS-WiFiSelfTest", "FAILED isolated Wi-Fi trust test", error)
                runOnUiThread {
                    setContentView(TextView(this).apply { text = "Wi-Fi trust test failed: ${error.message}" })
                }
            } finally {
                worker.shutdown()
            }
        }
    }
}
