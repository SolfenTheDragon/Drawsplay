package com.drawsplay.tablet.video

/**
 * Builds the upstream Wi-Fi decode-statistics record: control subtype 8,
 * schema 1, followed by one single-line JSON object in the USB stats schema
 * so the host's adaptive bitrate consumes it unchanged. Not thread-safe.
 */
class WiFiVideoStats {
    data class Counters(
        val received: Long,
        val rendered: Long,
        val decodeDrops: Long,
        val queueDropped: Long,
        val queued: Int
    )

    /** The tablet's current Wi-Fi association, shown live in the host web UI. */
    data class Link(val rssiDbm: Int, val linkMbps: Int, val frequencyMhz: Int)

    private var last = Counters(0, 0, 0, 0, 0)
    private var discards = 0L

    /** True once per new queue discard: decoding is suspended until an IDR. */
    fun keyframeNeeded(discardEvents: Long): Boolean {
        val needed = discardEvents > discards
        discards = discardEvents
        return needed
    }

    fun next(now: Counters, intervalMs: Long, foreground: Boolean, link: Link? = null): ByteArray {
        val stale = now.decodeDrops - last.decodeDrops
        val drops = stale + (now.queueDropped - last.queueDropped)
        val json = "{\"type\":\"stats\",\"intervalMs\":$intervalMs" +
            ",\"framesReceived\":${now.received - last.received}" +
            ",\"framesRendered\":${now.rendered - last.rendered}" +
            ",\"avgDecodeMs\":0,\"queuedFrames\":${now.queued}" +
            ",\"frameDrops\":$drops,\"staleFrames\":$stale,\"appForeground\":$foreground" +
            (link?.let { ",\"wifiRssiDbm\":${it.rssiDbm},\"wifiLinkMbps\":${it.linkMbps},\"wifiFrequencyMhz\":${it.frequencyMhz}" } ?: "") +
            "}"
        last = now
        return PREFIX + json.toByteArray(Charsets.UTF_8)
    }

    companion object {
        private val PREFIX = byteArrayOf(8, 0, 0, 1)
        /** Control subtype 9, schema 1: ask the host for a fresh IDR. */
        val KEYFRAME_REQUEST = byteArrayOf(9, 0, 0, 1)
    }
}
