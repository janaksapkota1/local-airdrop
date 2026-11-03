package com.janak.homedrop

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.media.MediaScannerConnection
import android.net.wifi.WifiManager
import android.os.Build
import android.os.IBinder
import org.json.JSONObject
import java.io.File
import java.net.HttpURLConnection
import java.net.URL
import kotlin.concurrent.thread

/**
 * Keeps HomeDrop discoverable and receiving in the background:
 *  - runs the Go core inside a foreground service,
 *  - holds a Wi-Fi multicast lock (Android drops multicast without it),
 *  - turns incoming offers into notifications with Accept / Decline,
 *  - makes received files show up in the Files/Gallery apps.
 */
class HomeDropService : Service() {
    private var multicastLock: WifiManager.MulticastLock? = null
    private var wifiLock: WifiManager.WifiLock? = null
    @Volatile private var running = true

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        createChannels(this)
        val n = Notification.Builder(this, CH_SERVICE)
            .setSmallIcon(android.R.drawable.stat_sys_download_done)
            .setContentTitle("HomeDrop is ready to receive")
            .setContentIntent(openAppIntent(this))
            .setOngoing(true)
            .build()
        if (Build.VERSION.SDK_INT >= 34) {
            startForeground(1, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(1, n)
        }

        val wifi = applicationContext.getSystemService(Context.WIFI_SERVICE) as WifiManager
        multicastLock = wifi.createMulticastLock("homedrop").apply { setReferenceCounted(false); acquire() }
        @Suppress("DEPRECATION")
        wifiLock = wifi.createWifiLock(WifiManager.WIFI_MODE_FULL_HIGH_PERF, "homedrop")
            .apply { setReferenceCounted(false); acquire() }

        thread(name = "homedrop-events", isDaemon = true) { eventLoop() }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int = START_STICKY

    override fun onDestroy() {
        running = false
        multicastLock?.release()
        wifiLock?.release()
        super.onDestroy()
    }

    /** Follows the core's event stream; reconnects if it drops. */
    private fun eventLoop() {
        while (running) {
            try {
                Core.ensureStarted(applicationContext)
                val c = URL("http://127.0.0.1:${Core.port}/api/events").openConnection() as HttpURLConnection
                c.setRequestProperty("Authorization", "Bearer ${Core.token}")
                c.readTimeout = 60_000 // the core pings every 15 s
                c.inputStream.bufferedReader().useLines { lines ->
                    for (line in lines) {
                        if (!running) break
                        if (line.startsWith("data: ")) handleEvent(JSONObject(line.substring(6)))
                    }
                }
            } catch (_: Exception) {
            }
            Thread.sleep(1000)
        }
    }

    private fun handleEvent(e: JSONObject) {
        val nm = getSystemService(NotificationManager::class.java)
        when (e.getString("type")) {
            "offer" -> if (!MainActivity.inForeground) {
                val o = e.getJSONObject("data")
                nm.notify(o.getString("id").hashCode(), offerNotification(o))
            }
            "offer_closed" -> nm.cancel(e.getJSONObject("data").getString("id").hashCode())
            "chat" -> {
                val d = e.getJSONObject("data")
                val m = d.getJSONObject("message")
                val isNewIncoming = !m.optBoolean("out") && m.optString("status") == "received"
                if (isNewIncoming && !MainActivity.inForeground && lastNotified.add(m.getString("id"))) {
                    nm.notify(d.getString("peerId").hashCode(), chatNotification(d, m))
                }
            }
            "chat_read" -> nm.cancel(e.getJSONObject("data").getString("peerId").hashCode())
            "transfer" -> {
                val t = e.getJSONObject("data")
                if (t.optString("direction") == "receive" && t.optString("state") == "done") scanReceived(t)
            }
        }
    }

    private val lastNotified = java.util.Collections.synchronizedSet(mutableSetOf<String>())

    private fun chatNotification(d: JSONObject, m: JSONObject): Notification {
        val body = if (m.optString("kind") == "files") {
            val f = m.getJSONObject("files")
            if (f.optInt("fileCount") == 1) "Sent you ${f.getJSONArray("roots").optString(0)}"
            else "Sent you ${f.optInt("fileCount")} files"
        } else m.optString("text")
        val unread = d.optInt("unread")
        return Notification.Builder(this, CH_CHAT)
            .setSmallIcon(android.R.drawable.stat_notify_chat)
            .setContentTitle(d.optString("peerName"))
            .setContentText(body)
            .setStyle(Notification.BigTextStyle().bigText(body))
            .setNumber(unread)
            .setContentIntent(openAppIntent(this))
            .setCategory(Notification.CATEGORY_MESSAGE)
            .setAutoCancel(true)
            .build()
    }

    private fun offerNotification(o: JSONObject): Notification {
        val id = o.getString("id")
        val count = o.optInt("fileCount")
        val what = when {
            count == 0 -> "a text message"
            count == 1 -> o.getJSONArray("roots").optString(0)
            else -> "$count files"
        }
        val code = if (!o.optBoolean("trusted")) " · code ${o.optString("pairCode")}" else ""
        fun action(label: String, accept: Boolean): Notification.Action {
            val i = Intent(this, OfferActionReceiver::class.java)
                .putExtra("id", id).putExtra("accept", accept)
            val pi = PendingIntent.getBroadcast(
                this, (id + accept).hashCode(), i,
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
            )
            return Notification.Action.Builder(null, label, pi).build()
        }
        return Notification.Builder(this, CH_OFFERS)
            .setSmallIcon(android.R.drawable.stat_sys_download)
            .setContentTitle("${o.optString("senderName")} wants to send you")
            .setContentText(what + code)
            .setContentIntent(openAppIntent(this))
            .setCategory(Notification.CATEGORY_CALL)
            .addAction(action("Decline", false))
            .addAction(action("Accept", true))
            .setAutoCancel(true)
            .build()
    }

    /** Tell MediaStore about new files so the Files/Gallery apps show them. */
    private fun scanReceived(t: JSONObject) {
        val dir = t.optString("dir").ifEmpty { return }
        val roots = t.optJSONArray("roots") ?: return
        val files = (0 until roots.length())
            .flatMap { File(dir, roots.getString(it)).walkTopDown().filter { f -> f.isFile }.toList() }
            .map { it.absolutePath }
        if (files.isNotEmpty()) MediaScannerConnection.scanFile(this, files.toTypedArray(), null, null)
    }

    companion object {
        const val CH_SERVICE = "service"
        const val CH_OFFERS = "offers"
        const val CH_CHAT = "chat"

        fun start(ctx: Context) {
            ctx.startForegroundService(Intent(ctx, HomeDropService::class.java))
        }

        fun createChannels(ctx: Context) {
            val nm = ctx.getSystemService(NotificationManager::class.java)
            nm.createNotificationChannel(
                NotificationChannel(CH_SERVICE, "Background service", NotificationManager.IMPORTANCE_MIN)
            )
            nm.createNotificationChannel(
                NotificationChannel(CH_OFFERS, "Incoming transfers", NotificationManager.IMPORTANCE_HIGH)
            )
            nm.createNotificationChannel(
                NotificationChannel(CH_CHAT, "Messages", NotificationManager.IMPORTANCE_HIGH)
            )
        }

        fun openAppIntent(ctx: Context): PendingIntent = PendingIntent.getActivity(
            ctx, 0, Intent(ctx, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_IMMUTABLE
        )
    }
}

/** Handles Accept / Decline taps on an offer notification. */
class OfferActionReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        val id = intent.getStringExtra("id") ?: return
        val accept = intent.getBooleanExtra("accept", false)
        ctx.getSystemService(NotificationManager::class.java).cancel(id.hashCode())
        val pending = goAsync()
        thread {
            try {
                Core.post("/api/offers/$id", """{"accept":$accept}""")
            } catch (_: Exception) {
            } finally {
                pending.finish()
            }
        }
    }
}
