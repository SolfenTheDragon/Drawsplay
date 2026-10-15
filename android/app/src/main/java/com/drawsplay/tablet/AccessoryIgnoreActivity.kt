package com.drawsplay.tablet

import android.app.Activity
import android.os.Bundle

/** Handles the attach event for the no-auto-open accessory model by doing nothing. */
class AccessoryIgnoreActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        finish()
    }
}
