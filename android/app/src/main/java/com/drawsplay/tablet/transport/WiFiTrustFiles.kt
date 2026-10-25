package com.drawsplay.tablet.transport

import java.io.File
import java.io.IOException

/** AtomicFile's base, backup and pending write, accessible even with a broken Keystore. */
internal class WiFiTrustFiles(file: File) {
    private val files = listOf(file, File(file.path + ".bak"), File(file.path + ".new"))

    fun hasSavedHost(): Boolean = files.any { it.exists() }

    fun revoke() {
        for (file in files) {
            if (file.exists() && !file.delete()) {
                throw IOException("Cannot remove Wi-Fi trust; stop Wi-Fi and retry")
            }
        }
        if (hasSavedHost()) throw IOException("Wi-Fi trust still exists; stop Wi-Fi and retry")
    }
}
