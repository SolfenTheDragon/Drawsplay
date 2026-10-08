package com.drawsplay.tablet.video

/**
 * Chooses a safe low-latency recovery point when MediaCodec's input queue
 * backs up. Data is only retained from the start of a random-access picture;
 * otherwise the caller waits for the next one instead of feeding a damaged
 * prediction chain.
 */
internal object DecoderBacklogPolicy {
    internal enum class ParameterSet {
        VPS,
        SPS,
        PPS
    }

    internal data class NalInfo(
        val parameterSet: ParameterSet? = null,
        val isRandomAccessStart: Boolean = false,
        /** First slice of a picture: one per decoded frame. */
        val startsPicture: Boolean = false,
        /**
         * A random-access picture that needs nothing decoded before it: AVC
         * IDR, HEVC IDR/BLA. HEVC CRA is excluded because its leading
         * pictures may still reference what came before it.
         */
        val isCleanRandomAccessStart: Boolean = false
    )

    internal data class TrimResult(
        val retained: List<ByteArray>,
        val awaitRandomAccess: Boolean
    )

    internal data class SkipResult(
        val retained: List<ByteArray>,
        val droppedPictures: Int
    )

    fun inspect(nal: ByteArray, codec: String): NalInfo {
        val offset = annexBHeaderOffset(nal)
        if (offset < 0 || nal.size <= offset) return NalInfo()

        return if (codec == "hevc") {
            val type = (nal[offset].toInt() ushr 1) and 0x3F
            val parameterSet = when (type) {
                HEVC_NAL_VPS -> ParameterSet.VPS
                HEVC_NAL_SPS -> ParameterSet.SPS
                HEVC_NAL_PPS -> ParameterSet.PPS
                else -> null
            }
            val startsPicture = type in HEVC_VCL_TYPES && firstSliceFlag(nal, offset + 2)
            NalInfo(
                parameterSet = parameterSet,
                isRandomAccessStart = startsPicture && type in HEVC_RANDOM_ACCESS_TYPES,
                startsPicture = startsPicture,
                isCleanRandomAccessStart = startsPicture && type in HEVC_CLEAN_RANDOM_ACCESS_TYPES
            )
        } else {
            val type = nal[offset].toInt() and 0x1F
            val parameterSet = when (type) {
                AVC_NAL_SPS -> ParameterSet.SPS
                AVC_NAL_PPS -> ParameterSet.PPS
                else -> null
            }
            val startsPicture = type in AVC_SLICE_HEADER_TYPES && firstSliceFlag(nal, offset + 1)
            val idrStart = startsPicture && type == AVC_NAL_IDR
            NalInfo(
                parameterSet = parameterSet,
                isRandomAccessStart = idrStart,
                startsPicture = startsPicture,
                isCleanRandomAccessStart = idrStart
            )
        }
    }

    fun startsPicture(nal: ByteArray, codec: String): Boolean = inspect(nal, codec).startsPicture

    fun countPictureStarts(nals: Iterable<ByteArray>, codec: String): Int =
        nals.count { startsPicture(it, codec) }

    /**
     * Drops every picture queued ahead of the newest clean random-access
     * picture, keeping the latest parameter sets seen before it. Returns
     * null when there is no such picture. The dropped pictures could only
     * be decoded to be thrown away: nothing after the keyframe needs them.
     */
    fun skipToCleanRandomAccess(pending: List<ByteArray>, codec: String): SkipResult? {
        val infos = pending.map { inspect(it, codec) }
        val recoveryIndex = infos.indexOfLast { it.isCleanRandomAccessStart }
        if (recoveryIndex < 0) return null
        val dropped = (0 until recoveryIndex).count { infos[it].startsPicture }
        return SkipResult(retainFrom(pending, infos, recoveryIndex), dropped)
    }

    fun trim(
        pending: List<ByteArray>,
        codec: String,
        maxPendingNals: Int
    ): TrimResult {
        require(maxPendingNals > 0)
        if (pending.size <= maxPendingNals) {
            return TrimResult(pending, awaitRandomAccess = false)
        }

        val infos = pending.map { inspect(it, codec) }
        val recoveryIndex = infos.indexOfLast { it.isRandomAccessStart }
        if (recoveryIndex < 0) {
            return TrimResult(emptyList(), awaitRandomAccess = true)
        }

        val retained = retainFrom(pending, infos, recoveryIndex)

        // Never cut through the recovered keyframe or a following access
        // unit merely to satisfy the bound. Waiting for the next keyframe is
        // both safer and lower-latency than retaining a still-stale GOP.
        return if (retained.size <= maxPendingNals) {
            TrimResult(retained, awaitRandomAccess = false)
        } else {
            TrimResult(emptyList(), awaitRandomAccess = true)
        }
    }

    /** The latest parameter sets before [recoveryIndex], then everything from it. */
    private fun retainFrom(
        pending: List<ByteArray>,
        infos: List<NalInfo>,
        recoveryIndex: Int
    ): List<ByteArray> {
        val latestParameterSets = mutableMapOf<ParameterSet, ByteArray>()
        for (i in 0 until recoveryIndex) {
            infos[i].parameterSet?.let { latestParameterSets[it] = pending[i] }
        }
        return buildList {
            PARAMETER_SET_ORDER.forEach { kind ->
                latestParameterSets[kind]?.let(::add)
            }
            addAll(pending.subList(recoveryIndex, pending.size))
        }
    }

    private fun annexBHeaderOffset(nal: ByteArray): Int {
        if (nal.size >= 4 &&
            nal[0] == 0.toByte() && nal[1] == 0.toByte() && nal[2] == 1.toByte()
        ) {
            return 3
        }
        if (nal.size >= 5 &&
            nal[0] == 0.toByte() && nal[1] == 0.toByte() &&
            nal[2] == 0.toByte() && nal[3] == 1.toByte()
        ) {
            return 4
        }
        return -1
    }

    /**
     * AVC's first_mb_in_slice ue(v) is zero exactly when its first bit is 1.
     * HEVC exposes first_slice_segment_in_pic_flag in the same bit position.
     */
    private fun firstSliceFlag(nal: ByteArray, payloadOffset: Int): Boolean {
        return nal.size > payloadOffset && nal[payloadOffset].toInt() and 0x80 != 0
    }

    private val PARAMETER_SET_ORDER =
        listOf(ParameterSet.VPS, ParameterSet.SPS, ParameterSet.PPS)

    private const val AVC_NAL_IDR = 5
    private const val AVC_NAL_SPS = 7
    private const val AVC_NAL_PPS = 8
    private const val HEVC_NAL_VPS = 32
    private const val HEVC_NAL_SPS = 33
    private const val HEVC_NAL_PPS = 34

    // Slice types whose header opens with first_mb_in_slice: non-IDR,
    // data partition A, IDR.
    private val AVC_SLICE_HEADER_TYPES = setOf(1, 2, AVC_NAL_IDR)
    private val HEVC_VCL_TYPES = 0..31
    private val HEVC_RANDOM_ACCESS_TYPES = 16..23

    // BLA_W_LP..IDR_N_LP: decoders discard a BLA's skipped leading pictures.
    private val HEVC_CLEAN_RANDOM_ACCESS_TYPES = 16..20
}

