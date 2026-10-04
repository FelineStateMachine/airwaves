package dev.airwaves.tv

import android.content.ActivityNotFoundException
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.util.Log
import android.webkit.JavascriptInterface
import java.util.concurrent.FutureTask
import java.util.concurrent.TimeUnit

/**
 * window.AirwavesAndroid, the page's way to the shell. Methods run on a
 * WebView binder thread; anything touching views goes to the UI thread.
 */
class Bridge(private val activity: MainActivity) {

    /** The saved server's base URL ("http://nas:8089"), or "". */
    @JavascriptInterface
    fun getServer(): String = Server.saved(activity) ?: ""

    /** Saves a server and loads its TV page. "" opens setup. False if it isn't an address. */
    @JavascriptInterface
    fun setServer(url: String?): Boolean {
        if (url.isNullOrBlank()) {
            activity.runOnUiThread { activity.showSetup(null) }
            return true
        }
        val server = Server.normalize(url) ?: return false
        activity.runOnUiThread { activity.useServer(server) }
        return true
    }

    /** Opens the native setup screen (server address). */
    @JavascriptInterface
    fun openSetup() {
        activity.runOnUiThread { activity.showSetup(null) }
    }

    /** The app's versionName, as in the user agent. */
    @JavascriptInterface
    fun getVersion(): String = BuildConfig.VERSION_NAME

    @JavascriptInterface
    fun copyText(text: String?): Boolean {
        val clip = activity.getSystemService(Context.CLIPBOARD_SERVICE) as? ClipboardManager ?: return false
        return try {
            clip.setPrimaryClip(ClipData.newPlainText("Airwaves", text ?: ""))
            true
        } catch (e: Exception) {
            Log.w(TAG, "copyText: $e")
            false
        }
    }

    /** Opens a link in whatever app handles it. False if none does (Google TV often has no browser). */
    @JavascriptInterface
    fun openUrl(url: String?): Boolean {
        val uri = url?.let { Uri.parse(it.trim()) } ?: return false
        val task = FutureTask { activity.openExternal(uri) }
        activity.runOnUiThread(task)
        return try {
            task.get(3, TimeUnit.SECONDS)
        } catch (e: Exception) {
            false
        }
    }

    /** Logs to logcat under the tag Airwaves. level: debug, info, warn or error. */
    @JavascriptInterface
    fun log(level: String?, msg: String?) {
        val m = msg ?: ""
        when (level?.lowercase()) {
            "debug", "verbose", "trace" -> Log.d(TAG, m)
            "warn", "warning" -> Log.w(TAG, m)
            "error" -> Log.e(TAG, m)
            else -> Log.i(TAG, m)
        }
    }

    /** Closes the app. */
    @JavascriptInterface
    fun exit() {
        activity.runOnUiThread { activity.finish() }
    }

    companion object {
        const val TAG = "Airwaves"
        const val NAME = "AirwavesAndroid"

        /** Schemes openUrl and links never hand to other apps. */
        private val BLOCKED = setOf("file", "content", "javascript", "data", "intent", "about", "chrome")

        /** Answers links on TVs without a browser, with a "no app" message. */
        private const val STUBS = "com.android.tv.frameworkpackagestubs"

        /** ACTION_VIEW for a link from the page; false if it's refused or nothing handles it. */
        fun view(ctx: Context, uri: Uri): Boolean {
            val scheme = uri.scheme?.lowercase() ?: return false
            if (scheme in BLOCKED) return false
            val intent = Intent(Intent.ACTION_VIEW, uri).addCategory(Intent.CATEGORY_BROWSABLE)
            val handlers = ctx.packageManager.queryIntentActivities(intent, PackageManager.MATCH_DEFAULT_ONLY)
            if (handlers.isNotEmpty() && handlers.all { it.activityInfo.packageName == STUBS }) {
                Log.i(TAG, "no app opens $uri")
                return false
            }
            return try {
                ctx.startActivity(intent)
                true
            } catch (e: ActivityNotFoundException) {
                Log.i(TAG, "no app opens $uri")
                false
            } catch (e: SecurityException) {
                Log.w(TAG, "can't open $uri: $e")
                false
            }
        }
    }
}
