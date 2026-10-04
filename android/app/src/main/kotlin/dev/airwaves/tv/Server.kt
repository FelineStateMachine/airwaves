package dev.airwaves.tv

import android.content.Context
import org.json.JSONObject
import java.io.IOException
import java.net.ConnectException
import java.net.HttpURLConnection
import java.net.NoRouteToHostException
import java.net.SocketTimeoutException
import java.net.URI
import java.net.URL
import java.net.UnknownHostException
import javax.net.ssl.SSLException

/** The Airwaves server: its address, where it's saved, and the check before saving one. */
object Server {
    /** Where airwavesd listens (internal/api.DefaultPort). */
    const val DEFAULT_PORT = 8089

    private const val PREFS = "airwaves"
    private const val KEY = "server"

    fun saved(ctx: Context): String? =
        ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getString(KEY, null)?.takeIf { it.isNotEmpty() }

    fun save(ctx: Context, server: String) {
        ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putString(KEY, server).apply()
    }

    /** The TV page of a server. */
    fun tvUrl(server: String) = "$server/tv/"

    /**
     * A base URL like "http://nas:8089" from what was typed, as the desktop app
     * reads it: no scheme means http and, without a port, 8089. A pasted TV page
     * address ("http://nas:8089/tv/") gives its server. Null if it isn't one.
     */
    fun normalize(input: String): String? {
        var s = input.trim().trimEnd('/')
        if (s.isEmpty()) return null
        val bare = !s.contains("://")
        if (bare) s = "http://$s"
        val u = try {
            URI(s)
        } catch (e: Exception) {
            return null
        }
        val scheme = u.scheme?.lowercase()
        if (scheme != "http" && scheme != "https") return null
        val host = u.host ?: return null
        val port = if (u.port == -1 && bare) DEFAULT_PORT else u.port
        val path = (u.rawPath ?: "").trimEnd('/').removeSuffix("/tv").trimEnd('/')
        val h = if (host.contains(':') && !host.startsWith("[")) "[$host]" else host
        return "$scheme://$h" + (if (port == -1) "" else ":$port") + path
    }

    /** host:port of a server, for messages. */
    fun label(server: String): String = server.substringAfter("://")

    sealed class Check {
        data class Ok(val name: String, val version: String) : Check()
        data class Failed(val reason: String) : Check()
    }

    /** Asks a server for /api/info. Blocks: call it off the UI thread. */
    fun check(server: String): Check {
        val where = label(server)
        val host = try {
            URI(server).host ?: where
        } catch (e: Exception) {
            where
        }
        val conn = try {
            URL("$server/api/info").openConnection() as HttpURLConnection
        } catch (e: Exception) {
            return Check.Failed("Not an address: $server")
        }
        return try {
            conn.connectTimeout = 6000
            conn.readTimeout = 10000
            conn.setRequestProperty("Accept", "application/json")
            val code = conn.responseCode
            when {
                code == 401 || code == 403 -> Check.Failed("$where wants a password (HTTP $code).")
                code != 200 -> Check.Failed("$where answered HTTP $code: not an Airwaves server?")
                else -> {
                    val body = conn.inputStream.bufferedReader().use { it.readText() }
                    val info = try {
                        JSONObject(body)
                    } catch (e: Exception) {
                        null
                    }
                    if (info == null || !info.has("version")) {
                        Check.Failed("$where answered, but it isn't an Airwaves server.")
                    } else {
                        Check.Ok(info.optString("name"), info.optString("version"))
                    }
                }
            }
        } catch (e: UnknownHostException) {
            Check.Failed("Can't find $host. Check the name, or use its IP address.")
        } catch (e: ConnectException) {
            Check.Failed("$where refused the connection. Is airwavesd running?")
        } catch (e: NoRouteToHostException) {
            Check.Failed("No route to $host.")
        } catch (e: SocketTimeoutException) {
            Check.Failed("No answer from $where.")
        } catch (e: SSLException) {
            Check.Failed("TLS error from $where: ${e.message}")
        } catch (e: IOException) {
            Check.Failed("Can't reach $where: ${e.message ?: e.javaClass.simpleName}")
        } finally {
            conn.disconnect()
        }
    }
}
