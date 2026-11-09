package com.drawsplay.tablet.update

import android.app.Activity
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInfo
import android.content.pm.PackageInstaller
import android.content.pm.PackageManager
import android.net.ConnectivityManager
import android.net.Uri
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.util.Log
import org.json.JSONObject
import java.io.File
import java.io.IOException
import java.io.InputStream
import java.net.HttpURLConnection
import java.net.URL
import java.security.MessageDigest
import java.util.concurrent.Executors

/** What the waiting screen shows about app updates. */
sealed class UpdateState {
    /** Nothing to show: up to date, not checked, or not a release build. */
    object None : UpdateState()
    /** A newer release exists; it downloads on tap (metered network). */
    data class Available(val version: String) : UpdateState()
    data class Downloading(val version: String, val percent: Int) : UpdateState()
    /** Downloaded and verified; installs on tap. */
    data class Ready(val version: String) : UpdateState()
    /** Android needs "Install unknown apps" allowed for DrawSplay first. */
    data class NeedsPermission(val version: String) : UpdateState()
    data class Installing(val version: String) : UpdateState()
    data class Failed(val version: String, val message: String) : UpdateState()
}

/**
 * Updates the app from the project's GitHub releases.
 *
 * While the app is in the foreground it checks the latest release at most
 * every [CHECK_INTERVAL_MS]. A newer DrawSplay.APK downloads by itself on an
 * unmetered network (on tap otherwise) and is accepted only if it matches the
 * release's SHA256SUMS.txt, is this package with a higher versionCode, and is
 * signed with the installed app's key. Installing always starts from a tap
 * on the waiting screen, so an update never interrupts a session; Android
 * then asks once to confirm (on Android 12+ it can skip that once DrawSplay
 * installed its own previous update). Only release builds (versionName
 * MAJOR.MINOR.PATCH) update themselves.
 */
object AppUpdater {
    private const val TAG = "DrawSplayUpdate"
    private const val LATEST_URL = "https://api.github.com/repos/SolfenTheDragon/Drawsplay/releases/latest"
    private const val APK_ASSET = "DrawSplay.APK"
    private const val SUMS_ASSET = "SHA256SUMS.txt"
    private const val CHECK_INTERVAL_MS = 6 * 60 * 60 * 1000L
    private const val MAX_JSON_BYTES = 2L shl 20
    private const val MAX_SUMS_BYTES = 64L shl 10
    private const val MAX_APK_BYTES = 512L shl 20
    private const val PREFS = "update"
    private const val KEY_LAST_CHECK = "lastCheck"
    const val ACTION_INSTALL_STATUS = "com.drawsplay.tablet.update.INSTALL_STATUS"

    private val worker = Executors.newSingleThreadExecutor { r -> Thread(r, "drawsplay-update").apply { isDaemon = true } }
    private val main = Handler(Looper.getMainLooper())

    // Main thread only.
    private var state: UpdateState = UpdateState.None
    private var listener: ((UpdateState) -> Unit)? = null
    private var busy = false
    private var release: Release? = null

    private class Release(val version: ReleaseVersion, val apkUrl: String, val apkSize: Long, val sumsUrl: String)

    fun setListener(l: ((UpdateState) -> Unit)?) {
        listener = l
        l?.invoke(state)
    }

    private fun publish(s: UpdateState) {
        main.post {
            state = s
            listener?.invoke(s)
        }
    }

    /** Called from onStart: checks for an update when one is due. */
    fun onForeground(context: Context) {
        val app = context.applicationContext
        val current = ReleaseVersion.parse(versionName(app)) ?: return
        val prefs = app.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val now = System.currentTimeMillis()
        val last = prefs.getLong(KEY_LAST_CHECK, 0)
        // Also check early when an earlier run left a verified download, so
        // its Install card comes back after the app restarts.
        val pendingDownload = release == null &&
            updateDir(app).listFiles()?.any { it.name.endsWith(".apk") } == true
        val due = now - last !in 0..CHECK_INTERVAL_MS || pendingDownload
        if (!due || busy || state is UpdateState.Installing) return
        busy = true
        prefs.edit().putLong(KEY_LAST_CHECK, now).apply()
        worker.execute {
            try {
                check(app, current)
            } catch (e: Exception) {
                // A failed check is not worth a message; the next start retries.
                Log.w(TAG, "Update check failed: $e")
                prefs.edit().putLong(KEY_LAST_CHECK, 0).apply()
            } finally {
                main.post { busy = false }
            }
        }
    }

    private fun check(app: Context, current: ReleaseVersion) {
        val json = JSONObject(String(fetch(LATEST_URL, MAX_JSON_BYTES), Charsets.UTF_8))
        if (json.optBoolean("draft") || json.optBoolean("prerelease")) return
        val latest = ReleaseVersion.parse(json.optString("tag_name")) ?: return
        if (latest <= current) {
            updateDir(app).listFiles()?.forEach { it.delete() } // an installed update
            publish(UpdateState.None)
            return
        }
        var apk: Pair<String, Long>? = null
        var sums: String? = null
        val assets = json.optJSONArray("assets")
        for (i in 0 until (assets?.length() ?: 0)) {
            val a = assets!!.getJSONObject(i)
            when (a.optString("name")) {
                APK_ASSET -> apk = a.optString("browser_download_url") to a.optLong("size")
                SUMS_ASSET -> sums = a.optString("browser_download_url")
            }
        }
        if (apk == null || sums == null) {
            Log.w(TAG, "Release $latest has no $APK_ASSET or $SUMS_ASSET")
            return
        }
        val rel = Release(latest, apk.first, apk.second, sums)
        main.post { release = rel }
        Log.i(TAG, "Update available: $latest (running $current)")
        if (readyFile(app, latest).isFile) {
            publish(UpdateState.Ready(latest.toString()))
        } else if (unmetered(app)) {
            download(app, rel)
        } else {
            publish(UpdateState.Available(latest.toString()))
        }
    }

    /** The update card's button. */
    fun onAction(activity: Activity) {
        val rel = release ?: return
        when (state) {
            is UpdateState.Available, is UpdateState.Failed -> {
                if (busy) return
                busy = true
                val app = activity.applicationContext
                worker.execute {
                    try {
                        if (readyFile(app, rel.version).isFile) publish(UpdateState.Ready(rel.version.toString()))
                        else download(app, rel)
                    } finally {
                        main.post { busy = false }
                    }
                }
            }
            is UpdateState.Ready, is UpdateState.NeedsPermission -> install(activity, rel)
            else -> Unit
        }
    }

    private fun download(app: Context, rel: Release) {
        val version = rel.version.toString()
        val dir = updateDir(app)
        try {
            publish(UpdateState.Downloading(version, 0))
            val sums = parseSha256Sums(String(fetch(rel.sumsUrl, MAX_SUMS_BYTES), Charsets.UTF_8))
            val want = sums[APK_ASSET] ?: throw IOException("$SUMS_ASSET does not list $APK_ASSET")
            dir.listFiles()?.forEach { it.delete() } // older downloads
            val tmp = File(dir, "download.tmp")
            val digest = MessageDigest.getInstance("SHA-256")
            open(rel.apkUrl).use { (conn, input) ->
                val total = if (rel.apkSize > 0) rel.apkSize else conn.contentLengthLong
                if (total > MAX_APK_BYTES) throw IOException("the APK is too large")
                tmp.outputStream().use { out ->
                    val buf = ByteArray(64 shl 10)
                    var done = 0L
                    var lastPct = -1
                    while (true) {
                        val n = input.read(buf)
                        if (n < 0) break
                        done += n
                        if (done > MAX_APK_BYTES) throw IOException("the APK is too large")
                        out.write(buf, 0, n)
                        digest.update(buf, 0, n)
                        val pct = if (total > 0) (100 * done / total).toInt().coerceIn(0, 100) else 0
                        if (pct != lastPct) {
                            lastPct = pct
                            publish(UpdateState.Downloading(version, pct))
                        }
                    }
                    if (rel.apkSize > 0 && done != rel.apkSize) throw IOException("the download is incomplete")
                }
            }
            val got = digest.digest().joinToString("") { "%02x".format(it) }
            if (got != want) throw IOException("the download does not match its SHA-256 checksum")
            verifyApk(app, tmp, rel.version)
            if (!tmp.renameTo(readyFile(app, rel.version))) throw IOException("cannot store the download")
            Log.i(TAG, "Update ${rel.version} downloaded and verified")
            publish(UpdateState.Ready(version))
        } catch (e: Exception) {
            Log.w(TAG, "Update download failed: $e")
            dir.listFiles()?.forEach { it.delete() }
            publish(UpdateState.Failed(version, e.message ?: e.toString()))
        }
    }

    /** The APK must be this app, newer, and signed with the same key. */
    private fun verifyApk(app: Context, apk: File, expected: ReleaseVersion) {
        val pm = app.packageManager
        val candidate = archiveInfo(pm, apk.path) ?: throw IOException("the download is not a valid APK")
        val installed = packageInfo(pm, app.packageName)
        if (candidate.packageName != app.packageName) throw IOException("the APK is for another app")
        if (ReleaseVersion.parse(candidate.versionName) != expected) {
            throw IOException("the APK reports version ${candidate.versionName}, expected $expected")
        }
        if (versionCode(candidate) <= versionCode(installed)) throw IOException("the APK is not newer than this app")
        val (installedSigners, multiple) = signers(installed)
        val (candidateSigners, _) = signers(candidate)
        if (!signersCompatible(installedSigners, candidateSigners, multiple)) {
            throw IOException("this release is signed with a different key than the installed app, so Android cannot update it. Uninstall DrawSplay and install DrawSplay.APK from the release page")
        }
    }

    private fun install(activity: Activity, rel: Release) {
        val version = rel.version.toString()
        val apk = readyFile(activity, rel.version)
        if (!apk.isFile) {
            publish(UpdateState.Available(version))
            return
        }
        if (!activity.packageManager.canRequestPackageInstalls()) {
            publish(UpdateState.NeedsPermission(version))
            activity.startActivity(
                Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:${activity.packageName}"))
            )
            return
        }
        publish(UpdateState.Installing(version))
        val app = activity.applicationContext
        worker.execute {
            try {
                val installer = app.packageManager.packageInstaller
                val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL).apply {
                    setAppPackageName(app.packageName)
                    setSize(apk.length())
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
                        setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_NOT_REQUIRED)
                    }
                }
                val id = installer.createSession(params)
                installer.openSession(id).use { session ->
                    session.openWrite("DrawSplay.apk", 0, apk.length()).use { out ->
                        apk.inputStream().use { it.copyTo(out) }
                        session.fsync(out)
                    }
                    val intent = Intent(app, UpdateInstallReceiver::class.java).setAction(ACTION_INSTALL_STATUS)
                    // Mutable: the installer adds the status extras.
                    val flags = PendingIntent.FLAG_UPDATE_CURRENT or
                        (if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) PendingIntent.FLAG_MUTABLE else 0)
                    session.commit(PendingIntent.getBroadcast(app, id, intent, flags).intentSender)
                }
            } catch (e: Exception) {
                Log.w(TAG, "Update install failed: $e")
                publish(UpdateState.Failed(version, e.message ?: e.toString()))
            }
        }
    }

    /** From [UpdateInstallReceiver] when the installer gives up. */
    internal fun installFailed(message: String) {
        main.post {
            val rel = release ?: return@post
            publish(UpdateState.Failed(rel.version.toString(), message))
        }
    }

    /** The install was cancelled at Android's confirmation: offer it again. */
    internal fun installCancelled() {
        main.post {
            val rel = release ?: return@post
            publish(UpdateState.Ready(rel.version.toString()))
        }
    }

    // --- helpers ---

    private fun updateDir(app: Context) = File(app.cacheDir, "updates").apply { mkdirs() }

    private fun readyFile(app: Context, v: ReleaseVersion) = File(updateDir(app.applicationContext), "DrawSplay-$v.apk")

    private fun unmetered(app: Context): Boolean {
        val cm = app.getSystemService(ConnectivityManager::class.java) ?: return false
        return cm.activeNetwork != null && !cm.isActiveNetworkMetered
    }

    private class Opened(val conn: HttpURLConnection, val input: InputStream) : AutoCloseable {
        operator fun component1() = conn
        operator fun component2() = input
        override fun close() {
            input.close()
            conn.disconnect()
        }
    }

    private fun open(url: String): Opened {
        val u = URL(url)
        if (!updateUrlAllowed(u.protocol, u.host)) throw IOException("unexpected download address")
        val conn = u.openConnection() as HttpURLConnection
        conn.connectTimeout = 15_000
        conn.readTimeout = 30_000
        conn.instanceFollowRedirects = true // HTTPS to HTTPS only
        conn.setRequestProperty("User-Agent", "DrawSplay-android")
        conn.setRequestProperty("Accept", "application/vnd.github+json, application/octet-stream")
        val code = conn.responseCode
        if (!updateUrlAllowed(conn.url.protocol, conn.url.host)) {
            conn.disconnect()
            throw IOException("refusing redirect to ${conn.url.host}")
        }
        if (code != HttpURLConnection.HTTP_OK) {
            conn.disconnect()
            throw IOException("HTTP $code")
        }
        return Opened(conn, conn.inputStream)
    }

    private fun fetch(url: String, limit: Long): ByteArray = open(url).use { (_, input) ->
        val data = input.readNBytesCompat(limit + 1)
        if (data.size > limit) throw IOException("response too large")
        data
    }

    private fun InputStream.readNBytesCompat(max: Long): ByteArray {
        val out = java.io.ByteArrayOutputStream()
        val buf = ByteArray(8192)
        while (out.size() < max) {
            val n = read(buf, 0, minOf(buf.size.toLong(), max - out.size()).toInt())
            if (n < 0) break
            out.write(buf, 0, n)
        }
        return out.toByteArray()
    }

    private fun versionName(app: Context): String? = try {
        packageInfo(app.packageManager, app.packageName).versionName
    } catch (_: Exception) {
        null
    }

    private fun signingFlags(): Int =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) PackageManager.GET_SIGNING_CERTIFICATES
        else @Suppress("DEPRECATION") PackageManager.GET_SIGNATURES

    private fun packageInfo(pm: PackageManager, name: String): PackageInfo =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            pm.getPackageInfo(name, PackageManager.PackageInfoFlags.of(signingFlags().toLong()))
        } else {
            @Suppress("DEPRECATION")
            pm.getPackageInfo(name, signingFlags())
        }

    private fun archiveInfo(pm: PackageManager, path: String): PackageInfo? =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            pm.getPackageArchiveInfo(path, PackageManager.PackageInfoFlags.of(signingFlags().toLong()))
        } else {
            @Suppress("DEPRECATION")
            pm.getPackageArchiveInfo(path, signingFlags())
        }

    private fun versionCode(info: PackageInfo): Long =
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) info.longVersionCode
        else @Suppress("DEPRECATION") info.versionCode.toLong()

    /** SHA-256 digests of the signing certificates, and whether there are several signers. */
    private fun signers(info: PackageInfo): Pair<Set<String>, Boolean> {
        val sigs: Array<android.content.pm.Signature>
        val multiple: Boolean
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            val si = info.signingInfo ?: return emptySet<String>() to false
            multiple = si.hasMultipleSigners()
            sigs = (if (multiple) si.apkContentsSigners else si.signingCertificateHistory) ?: emptyArray()
        } else {
            @Suppress("DEPRECATION")
            sigs = info.signatures ?: emptyArray()
            multiple = sigs.size > 1
        }
        val md = MessageDigest.getInstance("SHA-256")
        return sigs.map { s -> md.digest(s.toByteArray()).joinToString("") { "%02x".format(it) } }.toSet() to multiple
    }
}
