package com.janak.homedrop

import android.content.Context
import android.os.Build
import android.os.Environment
import android.provider.Settings
import com.janak.homedrop.core.mobile.Mobile
import org.json.JSONObject
import java.io.File
import java.net.HttpURLConnection
import java.net.URL

/** Starts the Go core (once per process) and talks to its control API. */
object Core {
    @Volatile var port = 0; private set
    @Volatile var token = ""; private set

    @Synchronized
    fun ensureStarted(ctx: Context): String {
        val dataDir = File(ctx.filesDir, "homedrop").apply { mkdirs() }.absolutePath
        val downloads = File(
            Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS), "HomeDrop"
        ).absolutePath
        val ep = Mobile.start(dataDir, downloads, deviceName(ctx))
        val j = JSONObject(ep)
        port = j.getInt("port")
        token = j.getString("token")
        return ep
    }

    fun stop() = Mobile.stop()

    private fun deviceName(ctx: Context): String =
        Settings.Global.getString(ctx.contentResolver, "device_name")?.takeIf { it.isNotBlank() }
            ?: Build.MODEL

    /** POST to the control API (call off the main thread). */
    fun post(path: String, body: String) {
        val c = URL("http://127.0.0.1:$port$path").openConnection() as HttpURLConnection
        try {
            c.requestMethod = "POST"
            c.doOutput = true
            c.connectTimeout = 3000
            c.setRequestProperty("Authorization", "Bearer $token")
            c.setRequestProperty("Content-Type", "application/json")
            c.outputStream.use { it.write(body.toByteArray()) }
            c.responseCode
        } finally {
            c.disconnect()
        }
    }
}
