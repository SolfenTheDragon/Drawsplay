package com.drawsplay.tablet

/** A decoded picture alone does not give the remote computer ownership of local input. */
data class LocalUiPolicy(
    val foreground: Boolean,
    val resumed: Boolean,
    val picture: Boolean,
    val waiting: Boolean,
    val pairing: Boolean,
    val dialog: Boolean,
    val windowFocused: Boolean,
    val streamingViewFocused: Boolean
) {
    val remoteInput: Boolean
        get() = foreground && resumed && picture && !waiting && !pairing && !dialog &&
            windowFocused && streamingViewFocused

    val keepScreenOn: Boolean
        get() = foreground && resumed && ((picture && !waiting) || pairing)

    val highRefresh: Boolean
        get() = foreground && resumed && picture && !waiting && !pairing
}
