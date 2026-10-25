package com.drawsplay.tablet.transport

/** Process-owned so activity recreation cannot race an in-flight trust deletion. */
internal class WiFiHostResetState {
    private var epoch = 0L
    @Volatile var inProgress = false
        private set
    @Volatile var reconnectBlocked = false
        private set

    @Synchronized
    fun begin(): Boolean {
        if (inProgress) return false
        epoch++
        reconnectBlocked = true
        inProgress = true
        return true
    }

    @Synchronized
    fun finish() {
        inProgress = false
    }

    @Synchronized
    fun pairingEpoch(): Long? = if (inProgress) null else epoch

    @Synchronized
    fun paired(pairingEpoch: Long): Boolean {
        if (inProgress || pairingEpoch != epoch) return false
        reconnectBlocked = false
        return true
    }
}
