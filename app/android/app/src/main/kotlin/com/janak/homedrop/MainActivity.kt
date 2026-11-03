package com.janak.homedrop

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.OpenableColumns
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel
import java.io.File
import kotlin.concurrent.thread

class MainActivity : FlutterActivity() {
    private var shareChannel: MethodChannel? = null
    private var pendingShare: Map<String, Any?>? = null

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        val messenger = flutterEngine.dartExecutor.binaryMessenger

        MethodChannel(messenger, "homedrop/core").setMethodCallHandler { call, result ->
            when (call.method) {
                "start" -> thread {
                    try {
                        val ep = Core.ensureStarted(applicationContext)
                        HomeDropService.start(applicationContext)
                        runOnUiThread { result.success(ep) }
                    } catch (e: Exception) {
                        runOnUiThread { result.error("start", e.message, null) }
                    }
                }
                else -> result.notImplemented()
            }
        }

        shareChannel = MethodChannel(messenger, "homedrop/share").apply {
            setMethodCallHandler { call, result ->
                if (call.method == "initial") {
                    result.success(pendingShare)
                    pendingShare = null
                } else result.notImplemented()
            }
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        requestPermissionsIfNeeded()
        handleShare(intent, initial = true)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleShare(intent, initial = false)
    }

    override fun onResume() {
        super.onResume()
        inForeground = true
    }

    override fun onPause() {
        inForeground = false
        super.onPause()
    }

    private fun requestPermissionsIfNeeded() {
        val wanted = mutableListOf<String>()
        if (Build.VERSION.SDK_INT >= 33) wanted += Manifest.permission.POST_NOTIFICATIONS
        if (Build.VERSION.SDK_INT <= 28) wanted += Manifest.permission.WRITE_EXTERNAL_STORAGE
        val missing = wanted.filter { checkSelfPermission(it) != PackageManager.PERMISSION_GRANTED }
        if (missing.isNotEmpty()) requestPermissions(missing.toTypedArray(), 1)
    }

    /** "Share → HomeDrop" from another app: copy the shared items to files the core can read. */
    private fun handleShare(intent: Intent?, initial: Boolean) {
        if (intent == null || (intent.action != Intent.ACTION_SEND && intent.action != Intent.ACTION_SEND_MULTIPLE)) return
        val uris = mutableListOf<Uri>()
        if (intent.action == Intent.ACTION_SEND) {
            intent.parcelable<Uri>(Intent.EXTRA_STREAM)?.let { uris += it }
        } else {
            intent.parcelableList<Uri>(Intent.EXTRA_STREAM)?.let { uris += it }
        }
        val text = intent.getStringExtra(Intent.EXTRA_TEXT)?.takeIf { uris.isEmpty() }
        intent.action = null // don't handle it again on rotation

        thread {
            val dir = File(cacheDir, "shared/${System.currentTimeMillis()}").apply { mkdirs() }
            val paths = uris.mapNotNull { copyToCache(it, dir) }
            val payload = mapOf("paths" to paths, "text" to text)
            runOnUiThread {
                if (initial) pendingShare = payload else shareChannel?.invokeMethod("shared", payload)
            }
        }
    }

    private fun copyToCache(uri: Uri, dir: File): String? = try {
        var name = "file"
        contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use {
            if (it.moveToFirst()) name = it.getString(0) ?: name
        }
        name = name.replace('/', '_')
        var out = File(dir, name)
        var i = 1
        while (out.exists()) out = File(dir, "${name.substringBeforeLast('.')} (${i++}).${name.substringAfterLast('.', "")}".trimEnd('.'))
        contentResolver.openInputStream(uri)!!.use { input -> out.outputStream().use { input.copyTo(it, 1 shl 20) } }
        out.absolutePath
    } catch (_: Exception) {
        null
    }

    @Suppress("DEPRECATION")
    private inline fun <reified T : android.os.Parcelable> Intent.parcelable(key: String): T? =
        if (Build.VERSION.SDK_INT >= 33) getParcelableExtra(key, T::class.java) else getParcelableExtra(key)

    @Suppress("DEPRECATION")
    private inline fun <reified T : android.os.Parcelable> Intent.parcelableList(key: String): List<T>? =
        if (Build.VERSION.SDK_INT >= 33) getParcelableArrayListExtra(key, T::class.java) else getParcelableArrayListExtra(key)

    companion object {
        @Volatile var inForeground = false
    }
}
