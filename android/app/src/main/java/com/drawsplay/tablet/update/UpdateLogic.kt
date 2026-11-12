package com.drawsplay.tablet.update

/** A release version, MAJOR.MINOR.PATCH (tags are "v1.4.0", versionName is "1.4.0"). */
data class ReleaseVersion(val major: Int, val minor: Int, val patch: Int) : Comparable<ReleaseVersion> {
    override fun compareTo(other: ReleaseVersion): Int =
        compareValuesBy(this, other, { it.major }, { it.minor }, { it.patch })

    override fun toString() = "$major.$minor.$patch"

    companion object {
        private val pattern = Regex("""^v?(0|[1-9]\d{0,8})\.(0|[1-9]\d{0,8})\.(0|[1-9]\d{0,8})$""")

        /** Null for anything but a plain release version, such as "dev" builds. */
        fun parse(text: String?): ReleaseVersion? {
            val m = pattern.matchEntire(text?.trim() ?: return null) ?: return null
            val (major, minor, patch) = m.destructured
            return ReleaseVersion(major.toInt(), minor.toInt(), patch.toInt())
        }
    }
}

/** Reads sha256sum output ("<hex>  <name>" or "<hex> *<name>") into name -> lowercase digest. */
fun parseSha256Sums(text: String): Map<String, String> {
    val out = HashMap<String, String>()
    for (line in text.lineSequence()) {
        val fields = line.trim().split(Regex("\\s+"))
        if (fields.size != 2 || fields[0].length != 64 || !fields[0].all { it.isLetterOrDigit() }) continue
        out[fields[1].removePrefix("*")] = fields[0].lowercase()
    }
    return out
}

/** Downloads are accepted only over HTTPS from GitHub. */
fun updateUrlAllowed(scheme: String?, host: String?): Boolean {
    if (!scheme.equals("https", ignoreCase = true)) return false
    val h = host?.lowercase() ?: return false
    return h == "github.com" || h == "api.github.com" || h.endsWith(".githubusercontent.com")
}

/**
 * Ed25519 public key (hex) that signs each release's SHA256SUMS.txt; the same
 * key the PC host pins. With it, a compromised GitHub account alone cannot
 * publish a checksum list the app would trust.
 */
const val RELEASE_CHECKSUMS_PUBLIC_KEY_HEX = "247960eda0e2c352ac115ac991abcc45fccca85fb429bc0e5a54426ed4aec8f1"

/** Whether [signature] is a valid Ed25519 signature of [checksums] under [publicKeyHex]. */
fun verifyChecksumSignature(publicKeyHex: String, checksums: ByteArray, signature: ByteArray): Boolean {
    if (publicKeyHex.length != 64 || signature.size != 64) return false
    val key = try {
        publicKeyHex.chunked(2).map { it.toInt(16).toByte() }.toByteArray()
    } catch (_: NumberFormatException) {
        return false
    }
    return try {
        val signer = org.bouncycastle.crypto.signers.Ed25519Signer()
        signer.init(false, org.bouncycastle.crypto.params.Ed25519PublicKeyParameters(key, 0))
        signer.update(checksums, 0, checksums.size)
        signer.verifySignature(signature)
    } catch (_: Exception) {
        false
    }
}

/**
 * Whether an APK signed by [candidate] can update an app signed by [installed]
 * (certificate digests). With several signers Android needs the same set;
 * with one, the candidate's lineage must include the installed signer.
 */
fun signersCompatible(installed: Set<String>, candidate: Set<String>, multipleSigners: Boolean): Boolean {
    if (installed.isEmpty() || candidate.isEmpty()) return false
    return if (multipleSigners) installed == candidate else candidate.any { it in installed }
}
