package dev.airwaves.tv

import android.annotation.SuppressLint
import android.app.Activity
import android.content.Intent
import android.graphics.Bitmap
import android.graphics.Color
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.util.Log
import android.view.KeyEvent
import android.view.View
import android.view.ViewConfiguration
import android.view.ViewGroup
import android.view.WindowInsets
import android.view.WindowInsetsController
import android.view.WindowManager
import android.webkit.ConsoleMessage
import android.webkit.RenderProcessGoneDetail
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.FrameLayout
import android.widget.Toast

/**
 * Airwaves for Android TV: the server's TV page (<server>/tv/) in a fullscreen
 * WebView, and a native setup screen for the server address.
 */
class MainActivity : Activity() {
    private val ui = Handler(Looper.getMainLooper())
    private lateinit var root: FrameLayout
    private lateinit var setup: Setup
    private var web: WebView? = null

    /** The TV page is up: its main frame loaded without an error. */
    private var pageOk = false
    private var loadFailed = false
    private var resumed = false
    private var reloadOnResume = false
    private val crashes = ArrayDeque<Long>()

    private var backLong = false
    private var swallowBackUp = false
    private var backPending = 0
    private var lastExitPress = 0L
    private var checking = 0

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        setContentView(R.layout.main)
        root = findViewById(android.R.id.content)
        setup = Setup(findViewById(R.id.setup), ::connect, ::leaveSetup)
        if (BuildConfig.DEBUG) WebView.setWebContentsDebuggingEnabled(true)

        takeServerExtra(intent)
        val server = Server.saved(this)
        if (server == null) showSetup(null) else load(server)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        if (takeServerExtra(intent)) Server.saved(this)?.let { load(it) }
    }

    /** `am start ... --es server http://nas:8089` sets the server (scripts/android.sh). */
    private fun takeServerExtra(intent: Intent?): Boolean {
        val server = intent?.getStringExtra(EXTRA_SERVER)?.let { Server.normalize(it) } ?: return false
        intent.removeExtra(EXTRA_SERVER)
        Server.save(this, server)
        Log.i(TAG, "server set from the launch intent: $server")
        return true
    }

    override fun onResume() {
        super.onResume()
        resumed = true
        immersive()
        web?.let {
            it.onResume()
            if (!setup.visible) it.evaluateJavascript(RESUME_JS, null)
        }
        if (reloadOnResume) {
            reloadOnResume = false
            Server.saved(this)?.let { load(it) }
        }
    }

    override fun onPause() {
        resumed = false
        web?.let {
            it.evaluateJavascript(PAUSE_JS, null)
            it.onPause()
        }
        super.onPause()
    }

    override fun onDestroy() {
        ui.removeCallbacksAndMessages(null)
        dropWebView()
        super.onDestroy()
    }

    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        if (hasFocus) immersive()
    }

    private fun immersive() {
        if (Build.VERSION.SDK_INT >= 30) {
            @Suppress("DEPRECATION") // edge to edge is the default from API 35
            if (Build.VERSION.SDK_INT < 35) window.setDecorFitsSystemWindows(false)
            window.insetsController?.let {
                it.hide(WindowInsets.Type.systemBars())
                it.systemBarsBehavior = WindowInsetsController.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
            }
        } else {
            @Suppress("DEPRECATION")
            window.decorView.systemUiVisibility = (View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY
                or View.SYSTEM_UI_FLAG_FULLSCREEN or View.SYSTEM_UI_FLAG_HIDE_NAVIGATION
                or View.SYSTEM_UI_FLAG_LAYOUT_STABLE or View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN
                or View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION)
        }
    }

    // ---------- the page ----------

    /** Switches to a server (saved) and loads its TV page. */
    fun useServer(server: String) {
        Server.save(this, server)
        load(server)
    }

    private fun load(server: String) {
        setup.hide()
        pageOk = false
        val w = web ?: newWebView()
        w.visibility = View.VISIBLE
        w.loadUrl(Server.tvUrl(server))
        w.requestFocus()
    }

    @SuppressLint("SetJavaScriptEnabled")
    private fun newWebView(): WebView {
        val w = WebView(this)
        w.setBackgroundColor(Color.BLACK)
        w.isFocusable = true
        w.isFocusableInTouchMode = true
        w.defaultFocusHighlightEnabled = false
        w.isVerticalScrollBarEnabled = false
        w.isHorizontalScrollBarEnabled = false
        w.overScrollMode = View.OVER_SCROLL_NEVER
        w.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            mediaPlaybackRequiresUserGesture = false
            setSupportZoom(false)
            builtInZoomControls = false
            displayZoomControls = false
            textZoom = 100 // the TV layout ignores the system font scale
            allowFileAccess = false
            allowContentAccess = false
            setSupportMultipleWindows(false)
            userAgentString = "$userAgentString AirwavesTV/${BuildConfig.VERSION_NAME}"
        }
        w.webViewClient = Client()
        w.webChromeClient = Chrome()
        w.addJavascriptInterface(Bridge(this), Bridge.NAME)
        root.addView(w, 0, FrameLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT))
        if (!resumed) w.onPause()
        web = w
        return w
    }

    private fun dropWebView() {
        val w = web ?: return
        web = null
        root.removeView(w)
        w.destroy()
    }

    /** Opens a link outside, in whatever app handles it. */
    fun openExternal(uri: Uri): Boolean = Bridge.view(this, uri)

    private fun sameServer(uri: Uri): Boolean {
        val server = Server.saved(this)?.let { Uri.parse(it) } ?: return false
        return uri.scheme == server.scheme && uri.host == server.host && uri.port == server.port
    }

    private inner class Client : WebViewClient() {
        override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
            if (!request.isForMainFrame || request.isRedirect) return false
            val uri = request.url
            // The server's own pages stay here; links elsewhere go to other apps.
            if ((uri.scheme == "http" || uri.scheme == "https") && sameServer(uri)) return false
            if (!openExternal(uri)) Toast.makeText(this@MainActivity, "Nothing here opens $uri", Toast.LENGTH_LONG).show()
            return true
        }

        override fun onPageStarted(view: WebView, url: String?, favicon: Bitmap?) {
            loadFailed = false
        }

        override fun onPageFinished(view: WebView, url: String?) {
            if (view == web && !loadFailed) pageOk = true
        }

        override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
            if (!request.isForMainFrame || view != web) return
            failed("Couldn't load ${request.url}: ${error.description}")
        }

        override fun onReceivedHttpError(view: WebView, request: WebResourceRequest, response: WebResourceResponse) {
            if (!request.isForMainFrame || view != web) return
            val hint = if (response.statusCode == 404) " Does this server have the TV app?" else ""
            failed("${request.url} answered HTTP ${response.statusCode}.$hint")
        }

        override fun onRenderProcessGone(view: WebView, detail: RenderProcessGoneDetail): Boolean {
            Log.w(TAG, "the page's renderer " + if (detail.didCrash()) "crashed" else "was killed")
            if (view != web) return true
            dropWebView()
            val now = SystemClock.elapsedRealtime()
            crashes.addLast(now)
            while (crashes.first() < now - 60_000) crashes.removeFirst()
            when {
                crashes.size >= 3 -> showSetup("The TV page crashed ${crashes.size} times in a minute.")
                resumed -> Server.saved(this@MainActivity)?.let { load(it) }
                else -> reloadOnResume = true
            }
            return true
        }
    }

    private inner class Chrome : WebChromeClient() {
        /** No grey play-button poster on videos without one. */
        override fun getDefaultVideoPoster(): Bitmap = Bitmap.createBitmap(1, 1, Bitmap.Config.ARGB_8888)

        override fun onConsoleMessage(m: ConsoleMessage): Boolean {
            if (!BuildConfig.DEBUG && m.messageLevel() != ConsoleMessage.MessageLevel.ERROR) return true
            val line = "console: ${m.message()} (${m.sourceId()}:${m.lineNumber()})"
            when (m.messageLevel()) {
                ConsoleMessage.MessageLevel.ERROR -> Log.e(TAG, line)
                ConsoleMessage.MessageLevel.WARNING -> Log.w(TAG, line)
                else -> Log.d(TAG, line)
            }
            return true
        }
    }

    private fun failed(reason: String) {
        loadFailed = true
        pageOk = false
        Log.w(TAG, reason)
        setup.say(reason, error = true)
        showSetup(null)
    }

    // ---------- setup ----------

    /** Shows setup over the page (pausing it), with an error line if there's one. */
    fun showSetup(error: String?) {
        if (error != null) setup.say(error, error = true)
        web?.let {
            if (!setup.visible) it.evaluateJavascript(PAUSE_JS, null)
            it.visibility = View.INVISIBLE // nor focusable under setup
        }
        setup.show(Server.saved(this), canGoBack = pageOk && web != null)
    }

    /** Back to the page from setup, if it's up. */
    private fun leaveSetup() {
        val w = web
        if (!pageOk || w == null) return
        checking++
        setup.hide()
        w.visibility = View.VISIBLE
        w.requestFocus()
        w.evaluateJavascript(RESUME_JS, null)
    }

    private fun connect(server: String) {
        val id = ++checking
        setup.busy(true)
        setup.say("Checking ${Server.label(server)} ...")
        Thread {
            val result = Server.check(server)
            ui.post {
                if (id != checking || isDestroyed) return@post
                when (result) {
                    is Server.Check.Ok -> {
                        setup.say("Found Airwaves ${result.version}" + if (result.name.isEmpty()) "" else " (${result.name})")
                        setup.busy(false)
                        useServer(server)
                    }
                    is Server.Check.Failed -> {
                        setup.say(result.reason, error = true)
                        setup.busy(false)
                    }
                }
            }
        }.start()
    }

    // ---------- keys ----------

    override fun dispatchKeyEvent(e: KeyEvent): Boolean {
        if (setup.visible || web == null) {
            if (e.keyCode == KeyEvent.KEYCODE_BACK) {
                if (e.action == KeyEvent.ACTION_UP && !e.isCanceled) {
                    if (swallowBackUp) swallowBackUp = false
                    else if (setup.visible && pageOk && web != null) leaveSetup()
                    else exitPress()
                }
                return true
            }
            return super.dispatchKeyEvent(e)
        }
        when (e.keyCode) {
            KeyEvent.KEYCODE_BACK -> return backKey(e)
            KeyEvent.KEYCODE_MENU, KeyEvent.KEYCODE_SETTINGS -> {
                if (e.action == KeyEvent.ACTION_UP) showSetup(null)
                return true
            }
        }
        // The rest go to the page. WebView names the remote's keys as the page
        // expects (ChannelUp, MediaPlayPause, Guide, Info, ClosedCaptionToggle,
        // digits). TV keys are ours even when the page ignores them, so none
        // falls through to another app's media session or the system's guide.
        return super.dispatchKeyEvent(e) || e.keyCode in TV_KEYS
    }

    /** Back asks the page (window.airwavesBack); a long press opens setup. */
    private fun backKey(e: KeyEvent): Boolean {
        when (e.action) {
            KeyEvent.ACTION_DOWN -> if (e.repeatCount == 0) {
                backLong = false
            } else if (!backLong && (e.isLongPress || e.eventTime - e.downTime >= ViewConfiguration.getLongPressTimeout())) {
                backLong = true
                swallowBackUp = true
                showSetup(null)
            }
            KeyEvent.ACTION_UP -> if (!backLong && !e.isCanceled) webBack()
        }
        return true
    }

    private fun webBack() {
        val w = web ?: return exitPress()
        val id = ++backPending
        // A page that doesn't answer (loading, hung) counts as "not handled".
        val timeout = Runnable { if (backPending == id) { backPending++; exitPress() } }
        ui.postDelayed(timeout, 1500)
        w.evaluateJavascript(BACK_JS) { result ->
            if (backPending != id) return@evaluateJavascript
            backPending++
            ui.removeCallbacks(timeout)
            if (result == "true") lastExitPress = 0 else exitPress()
        }
    }

    /** Back with nothing left to close: twice within 2 s exits. */
    private fun exitPress() {
        val now = SystemClock.uptimeMillis()
        if (now - lastExitPress < 2000) {
            finish()
            return
        }
        lastExitPress = now
        Toast.makeText(this, R.string.press_back_again, Toast.LENGTH_SHORT).show()
    }

    companion object {
        private const val TAG = Bridge.TAG
        const val EXTRA_SERVER = "server"

        /** Remote keys the app keeps, handled by the page or not. */
        private val TV_KEYS = setOf(
            KeyEvent.KEYCODE_MEDIA_PLAY_PAUSE, KeyEvent.KEYCODE_MEDIA_PLAY, KeyEvent.KEYCODE_MEDIA_PAUSE,
            KeyEvent.KEYCODE_MEDIA_STOP, KeyEvent.KEYCODE_MEDIA_FAST_FORWARD, KeyEvent.KEYCODE_MEDIA_REWIND,
            KeyEvent.KEYCODE_MEDIA_NEXT, KeyEvent.KEYCODE_MEDIA_PREVIOUS, KeyEvent.KEYCODE_MEDIA_RECORD,
            KeyEvent.KEYCODE_MEDIA_AUDIO_TRACK, KeyEvent.KEYCODE_CHANNEL_UP, KeyEvent.KEYCODE_CHANNEL_DOWN,
            KeyEvent.KEYCODE_LAST_CHANNEL, KeyEvent.KEYCODE_GUIDE, KeyEvent.KEYCODE_INFO, KeyEvent.KEYCODE_CAPTIONS,
            KeyEvent.KEYCODE_DVR, KeyEvent.KEYCODE_PROG_RED, KeyEvent.KEYCODE_PROG_GREEN,
            KeyEvent.KEYCODE_PROG_YELLOW, KeyEvent.KEYCODE_PROG_BLUE,
        )

        private const val BACK_JS =
            "(function(){try{return window.airwavesBack?window.airwavesBack()===true:false}catch(e){return false}})()"

        // Leaving the app: the page hears visibilitychange (WebView.onPause);
        // this also pauses what's playing, unless the page has its own hook.
        private const val PAUSE_JS =
            "(function(){if(window.airwavesPause){try{window.airwavesPause()}catch(e){}return}" +
                "document.querySelectorAll('video,audio').forEach(function(m){" +
                "if(!m.paused){m.dataset.awResume='1';m.pause()}})})()"
        private const val RESUME_JS =
            "(function(){if(window.airwavesResume){try{window.airwavesResume()}catch(e){}return}" +
                "document.querySelectorAll('[data-aw-resume]').forEach(function(m){" +
                "delete m.dataset.awResume;if(m.paused&&m.readyState>0){var p=m.play();if(p&&p.catch)p.catch(function(){})}})})()"
    }
}
