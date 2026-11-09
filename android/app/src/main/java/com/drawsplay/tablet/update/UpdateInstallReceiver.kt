package com.drawsplay.tablet.update

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.os.Build
import android.util.Log

/** Receives the result of the update's PackageInstaller session. */
class UpdateInstallReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != AppUpdater.ACTION_INSTALL_STATUS) return
        val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)
        when (status) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                // Android's own confirmation screen. The user just tapped
                // Install in the app, which is in the foreground.
                val confirm = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
                    intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent::class.java)
                } else {
                    @Suppress("DEPRECATION")
                    intent.getParcelableExtra(Intent.EXTRA_INTENT)
                }
                if (confirm == null) {
                    AppUpdater.installFailed("Android did not show the install confirmation")
                    return
                }
                confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                try {
                    context.startActivity(confirm)
                } catch (e: Exception) {
                    AppUpdater.installFailed(e.message ?: e.toString())
                }
            }
            // The app is replaced and this process ends; nothing to do.
            PackageInstaller.STATUS_SUCCESS -> Log.i("DrawSplayUpdate", "Update installed")
            PackageInstaller.STATUS_FAILURE_ABORTED -> AppUpdater.installCancelled()
            else -> {
                val message = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE) ?: "status $status"
                Log.w("DrawSplayUpdate", "Update install failed: $message")
                AppUpdater.installFailed(message)
            }
        }
    }
}
