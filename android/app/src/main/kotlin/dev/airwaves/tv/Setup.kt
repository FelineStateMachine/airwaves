package dev.airwaves.tv

import android.text.SpannableStringBuilder
import android.text.Spanned
import android.text.style.ForegroundColorSpan
import android.view.KeyEvent
import android.view.View
import android.view.inputmethod.EditorInfo
import android.widget.Button
import android.widget.EditText
import android.widget.TextView

/**
 * The setup screen: a server address, Connect, and a log of what happened,
 * like the desktop app's boot screen. D-pad friendly: the address field, then
 * the buttons to its right.
 */
class Setup(
    private val view: View,
    private val onConnect: (String) -> Unit,
    private val onBack: () -> Unit,
) {
    private val ctx = view.context
    private val server: EditText = view.findViewById(R.id.setup_server)
    private val connect: Button = view.findViewById(R.id.setup_connect)
    private val back: Button = view.findViewById(R.id.setup_back)
    private val log: TextView = view.findViewById(R.id.setup_log)
    private val lines = ArrayList<Pair<String, Boolean>>()

    val visible get() = view.visibility == View.VISIBLE

    init {
        view.findViewById<TextView>(R.id.setup_version).text =
            ctx.getString(R.string.setup_version, BuildConfig.VERSION_NAME)
        connect.setOnClickListener { submit() }
        back.setOnClickListener { onBack() }
        server.setOnEditorActionListener { _, action, event ->
            val enter = event != null && event.keyCode == KeyEvent.KEYCODE_ENTER && event.action == KeyEvent.ACTION_DOWN
            if (action == EditorInfo.IME_ACTION_GO || action == EditorInfo.IME_ACTION_DONE || enter) {
                submit()
                true
            } else {
                false
            }
        }
    }

    /** Shows setup with the address filled in; canGoBack offers a way back to a working page. */
    fun show(address: String?, canGoBack: Boolean) {
        if (!visible || server.text.isNullOrEmpty()) server.setText(address ?: "")
        back.visibility = if (canGoBack) View.VISIBLE else View.GONE
        busy(false)
        if (lines.isEmpty()) {
            say(if (address == null) "No server yet: enter its address." else "Server: ${Server.label(address)}")
        }
        view.visibility = View.VISIBLE
        (if (server.text.isNullOrEmpty()) server else connect).requestFocus()
    }

    fun hide() {
        view.visibility = View.GONE
    }

    fun busy(on: Boolean) {
        connect.isEnabled = !on
        server.isEnabled = !on
        if (!on && visible && !connect.hasFocus() && !back.hasFocus() && !server.hasFocus()) connect.requestFocus()
    }

    /** Adds a line to the log; the last few stay. */
    fun say(text: String, error: Boolean = false) {
        lines.add(text to error)
        while (lines.size > 4) lines.removeAt(0)
        val amber = ctx.getColor(R.color.amber)
        val red = ctx.getColor(R.color.red)
        val out = SpannableStringBuilder()
        for ((i, line) in lines.withIndex()) {
            if (i > 0) out.append('\n')
            val start = out.length
            out.append("> ")
            out.setSpan(ForegroundColorSpan(amber), start, out.length, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE)
            val s = out.length
            out.append(line.first)
            if (line.second) out.setSpan(ForegroundColorSpan(red), s, out.length, Spanned.SPAN_EXCLUSIVE_EXCLUSIVE)
        }
        log.text = out
    }

    private fun submit() {
        if (!connect.isEnabled) return
        val typed = server.text?.toString() ?: ""
        val normalized = Server.normalize(typed)
        if (normalized == null) {
            say(if (typed.isBlank()) "Enter the server's address first." else "Not an address: $typed", error = true)
            server.requestFocus()
            return
        }
        server.setText(normalized)
        onConnect(normalized)
    }
}
