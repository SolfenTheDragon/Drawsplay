package com.drawsplay.tablet.video

/**
 * How much optional tuning is put in the decoder's MediaFormat. Tablet SoCs
 * differ widely in which keys they accept (a rejected key fails configure()
 * with ERROR_UNSUPPORTED), so configuration walks from most to least tuned
 * until one is accepted.
 */
enum class FormatTier {
    /** Standard low-latency hint, priority/rate hints and vendor low-latency keys. */
    FULL,

    /** Standard hints only; no vendor-specific keys. */
    STANDARD,

    /** Priority/operating-rate hints without the low-latency key some decoders reject. */
    RATE_HINTS,

    /** Only the input buffer size and the codec-specific data. */
    BASIC,

    /** Codec-specific data only; what every conformant decoder accepts. */
    MINIMAL;

    val vendorKeys: Boolean get() = this == FULL
    val lowLatencyHint: Boolean get() = this == FULL || this == STANDARD
    val rateHints: Boolean get() = this == FULL || this == STANDARD || this == RATE_HINTS
    val maxInputSize: Boolean get() = this != MINIMAL
}

/** A decoder to try: [name] null means the platform default for the MIME type. */
data class DecoderCandidate(val name: String?, val software: Boolean)

/** A decoder plus the format tier to configure it with. */
data class ConfigAttempt(val decoder: DecoderCandidate, val tier: FormatTier)

object DecoderConfigPlan {
    /**
     * Orders decoders: hardware first (platform default, then the others the
     * device lists), software last. [skipHardware] puts only software first
     * after hardware decoders kept failing at runtime.
     */
    fun orderCandidates(
        hardwareNames: List<String>,
        softwareNames: List<String>,
        skipHardware: Boolean
    ): List<DecoderCandidate> {
        val hardware = buildList {
            add(DecoderCandidate(null, software = false))
            hardwareNames.distinct().forEach { add(DecoderCandidate(it, software = false)) }
        }
        val software = softwareNames.distinct().map { DecoderCandidate(it, software = true) } +
            DecoderCandidate(null, software = true)
        return if (skipHardware) software + hardware else hardware + software
    }

    /**
     * Every attempt in order: a decoder is only abandoned once all format
     * tiers failed on it. [preferred], a combination that worked earlier in
     * this process, goes first.
     */
    fun attempts(candidates: List<DecoderCandidate>, preferred: ConfigAttempt?): List<ConfigAttempt> {
        val all = candidates.flatMap { c -> FormatTier.values().map { ConfigAttempt(c, it) } }
        if (preferred == null || preferred !in all) return all
        return listOf(preferred) + all.filter { it != preferred }
    }
}
