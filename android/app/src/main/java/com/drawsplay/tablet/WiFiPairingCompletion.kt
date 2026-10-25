package com.drawsplay.tablet

import java.io.IOException
import java.io.InputStream
import java.io.OutputStream

internal object WiFiPairingCompletion {
    /** Called only after authenticated approval and durable local trust storage. */
    fun confirm(input: InputStream, output: OutputStream, onPaired: () -> Boolean) {
        output.write(1)
        if (input.read() != 2) throw IOException("Computer did not confirm pairing")
        // Socket cancellation cannot undo a confirmed outcome. The process-owned
        // reset epoch, rather than a UI/socket generation, decides if it is current.
        if (!onPaired()) throw IOException("Pairing superseded by a local reset")
    }
}
