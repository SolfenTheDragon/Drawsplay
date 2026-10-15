package com.drawsplay.tablet.transport

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.hardware.usb.UsbAccessory
import android.hardware.usb.UsbManager
import android.os.Build
import android.os.ParcelFileDescriptor
import android.util.Log
import androidx.core.content.ContextCompat
import java.io.FileInputStream
import java.io.FileOutputStream
import java.io.IOException
import java.net.Socket
import java.util.concurrent.Executors

/**
 * Owns the USB accessory (AOA) connection to the host. No adb or Developer options needed:
 * the host switches the tablet into accessory mode and Android launches this app.
 * Process-wide because the input and video channels share the single link.
 */
object AccessoryTransport {
    private const val TAG = "AccessoryTransport"
    private const val ACTION_PERMISSION = "com.drawsplay.tablet.USB_ACCESSORY_PERMISSION"
    private const val MANUFACTURER = "DrawSplay"
    private const val MODEL = "Tablet"

    private val lock = Any()
    private val executor = Executors.newSingleThreadExecutor { r -> Thread(r, "accessory-open").apply { isDaemon = true } }

    private var appContext: Context? = null
    private var usbManager: UsbManager? = null
    private var receiver: BroadcastReceiver? = null
    private var availability: ((Boolean) -> Unit)? = null
    private var link: AccessoryLink? = null
    private var fd: ParcelFileDescriptor? = null
    private var permissionRequested = false

    fun start(context: Context, onAvailabilityChanged: (Boolean) -> Unit) {
        synchronized(lock) {
            if (receiver != null) return
            val ctx = context.applicationContext
            val manager = ctx.getSystemService(Context.USB_SERVICE) as? UsbManager ?: return
            appContext = ctx
            usbManager = manager
            availability = onAvailabilityChanged
            val r = object : BroadcastReceiver() {
                override fun onReceive(c: Context, intent: Intent) {
                    when (intent.action) {
                        UsbManager.ACTION_USB_ACCESSORY_DETACHED -> closeLink()
                        ACTION_PERMISSION -> {
                            synchronized(lock) { permissionRequested = false }
                            if (intent.getBooleanExtra(UsbManager.EXTRA_PERMISSION_GRANTED, false)) refresh()
                        }
                    }
                }
            }
            receiver = r
            val filter = IntentFilter().apply {
                addAction(UsbManager.ACTION_USB_ACCESSORY_DETACHED)
                addAction(ACTION_PERMISSION)
            }
            ContextCompat.registerReceiver(ctx, r, filter, ContextCompat.RECEIVER_NOT_EXPORTED)
        }
        refresh()
    }

    fun stop() {
        val ctx: Context?
        val r: BroadcastReceiver?
        synchronized(lock) {
            ctx = appContext
            r = receiver
            receiver = null
            availability = null
        }
        if (ctx != null && r != null) {
            try { ctx.unregisterReceiver(r) } catch (_: IllegalArgumentException) {}
        }
        closeLink()
    }

    /** Looks for an attached DrawSplay accessory and opens it. Safe to call repeatedly. */
    fun refresh() {
        executor.execute { openIfAttached() }
    }

    fun isAttached(): Boolean = synchronized(lock) { link != null }

    /** Opens a virtual connection; blocks, so call from a background thread only. */
    fun connect(kind: Int, timeoutMs: Long): Socket {
        val current = synchronized(lock) { link } ?: throw IOException("no USB accessory attached")
        return current.open(kind, timeoutMs)
    }

    private fun openIfAttached() {
        val manager: UsbManager
        val ctx: Context
        synchronized(lock) {
            if (link != null) return
            manager = usbManager ?: return
            ctx = appContext ?: return
        }
        val accessory = manager.accessoryList?.firstOrNull { it.manufacturer == MANUFACTURER && it.model == MODEL } ?: return
        if (!manager.hasPermission(accessory)) {
            requestPermission(ctx, manager, accessory)
            return
        }
        val pfd = try {
            manager.openAccessory(accessory)
        } catch (e: Exception) {
            Log.w(TAG, "openAccessory failed: ${e.message}")
            null
        } ?: return
        val newLink = AccessoryLink(FileInputStream(pfd.fileDescriptor), FileOutputStream(pfd.fileDescriptor)) { closed ->
            synchronized(lock) {
                if (link === closed) {
                    link = null
                    try { fd?.close() } catch (_: IOException) {}
                    fd = null
                }
            }
            notifyAvailability(false)
        }
        synchronized(lock) {
            fd = pfd
            link = newLink
        }
        newLink.start()
        Log.i(TAG, "USB accessory opened")
        notifyAvailability(true)
    }

    private fun requestPermission(ctx: Context, manager: UsbManager, accessory: UsbAccessory) {
        synchronized(lock) {
            if (permissionRequested) return
            permissionRequested = true
        }
        val intent = Intent(ACTION_PERMISSION).setPackage(ctx.packageName)
        val flags = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) PendingIntent.FLAG_MUTABLE else 0
        manager.requestPermission(accessory, PendingIntent.getBroadcast(ctx, 0, intent, flags))
    }

    private fun closeLink() {
        val current = synchronized(lock) { link }
        current?.close()
    }

    private fun notifyAvailability(available: Boolean) {
        val callback = synchronized(lock) { availability }
        callback?.invoke(available)
    }
}
