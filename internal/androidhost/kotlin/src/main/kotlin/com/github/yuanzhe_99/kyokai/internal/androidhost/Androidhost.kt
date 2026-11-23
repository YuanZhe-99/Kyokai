package com.github.yuanzhe_99.kyokai.internal.androidhost

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.ClipData
import android.content.ClipboardManager
import android.content.ContentProvider
import android.content.ContentValues
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.database.Cursor
import android.database.MatrixCursor
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.hardware.Sensor
import android.hardware.SensorEvent
import android.hardware.SensorEventListener
import android.hardware.SensorManager
import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioTrack
import android.media.MediaCodec
import android.media.MediaCodecList
import android.media.MediaFormat
import android.net.Uri
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.ParcelFileDescriptor
import android.os.PowerManager
import android.provider.OpenableColumns
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import android.view.MotionEvent
import android.view.SurfaceView
import android.view.View
import android.view.ViewGroup
import androidx.window.layout.FoldingFeature
import androidx.window.layout.WindowInfoTracker
import dev.mygo.MyGoActivity
import dev.mygo.Native
import dev.mygo.NativeCloser
import dev.mygo.NativeException
import java.io.ByteArrayOutputStream
import java.io.File
import java.io.FileNotFoundException
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.security.KeyStore
import java.security.SecureRandom
import java.util.concurrent.LinkedBlockingDeque
import java.util.concurrent.TimeUnit
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch

/** Creates the Android platform host bridge for Go. */
fun makeHost(): Host = AndroidHost()

private class AndroidHost : Host {
    @Volatile private var askedNotificationPerm = false
    private val mainHandler = Handler(Looper.getMainLooper())

    override fun info(): DeviceInfo {
        val ctx = Native.context
        val sensors = ctx.getSystemService(Context.SENSOR_SERVICE) as SensorManager
        val hasHinge = Build.VERSION.SDK_INT >= 30 && sensors.getDefaultSensor(Sensor.TYPE_HINGE_ANGLE) != null
        return DeviceInfo(
            androidVersion = Build.VERSION.RELEASE ?: "",
            sdk = Build.VERSION.SDK_INT.toLong(),
            manufacturer = Build.MANUFACTURER ?: "",
            model = Build.MODEL ?: "",
            abIs = Build.SUPPORTED_ABIS.joinToString(","),
            filesDir = ctx.filesDir.absolutePath,
            noBackupDir = ctx.noBackupFilesDir.absolutePath,
            cacheDir = ctx.cacheDir.absolutePath,
            nativeLibDir = ctx.applicationInfo.nativeLibraryDir ?: "",
            hasHingeSensor = hasHinge,
        )
    }

    override fun masterKey(): String = MasterKey.get(Native.context) ?: ""

    override fun decoders(): List<DecoderCap> = CodecProbe.decodable()

    override fun setSessions(text: String) {
        val ctx = Native.context
        Native.onMain {
            SessionService.update(ctx, text.ifEmpty { null })
            if (text.isNotEmpty() && !askedNotificationPerm && Build.VERSION.SDK_INT >= 33) {
                askedNotificationPerm = true
                mainHandler.postDelayed({
                    try {
                        val act = Native.activity
                        if (act != null &&
                            ctx.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
                        ) {
                            act.requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 0x4b59)
                        }
                    } catch (_: Throwable) {
                    }
                }, 400L)
            }
        }
    }

    override fun clipboardImage(unless: String): ClipImage = ClipSync.image(Native.context, unless)

    override fun setClipboardImage(png: ByteArray): String = ClipSync.setImage(Native.context, png)

    override fun watchPosture(onChange: (Posture) -> Unit): AutoCloseable {
        val watcher = PostureWatcher(Native.context, onChange)
        watcher.start()
        return NativeCloser { watcher.stop() }
    }

    override fun setTouchRegion(
        capture: Boolean,
        r1L: Double,
        r1T: Double,
        r1R: Double,
        r1B: Double,
        r2L: Double,
        r2T: Double,
        r2R: Double,
        r2B: Double,
        ex1L: Double,
        ex1T: Double,
        ex1R: Double,
        ex1B: Double,
        ex2L: Double,
        ex2T: Double,
        ex2R: Double,
        ex2B: Double,
        onTouch: (RawTouch) -> Unit,
    ) {
        TouchBridge.configure(capture, r1L, r1T, r1R, r1B, r2L, r2T, r2R, r2B, ex1L, ex1T, ex1R, ex1B, ex2L, ex2T, ex2R, ex2B, onTouch)
    }

    override fun openSound(): SoundPlayer = OpusSound.open() ?: throw NativeException("audio", -1, "cannot open Opus decoder")
}

/**
 * Intercepts raw multi-touch and mouse events over the remote-screen canvas
 * and split-keyboard touchpad on MyGo's SurfaceView before Surface.onTouchEvent
 * runs, so 1-, 2- and 3-finger gestures reach Go intact without MyGo's
 * scroll/pinch recognizer swallowing or cancelling them, while leaving
 * toolbar/zoom overlays untouched.
 */
private object TouchBridge {
    private val mainHandler = Handler(Looper.getMainLooper())
    @Volatile private var capture = false
    @Volatile private var r1L = 0.0
    @Volatile private var r1T = 0.0
    @Volatile private var r1R = 0.0
    @Volatile private var r1B = 0.0
    @Volatile private var r2L = 0.0
    @Volatile private var r2T = 0.0
    @Volatile private var r2R = 0.0
    @Volatile private var r2B = 0.0
    @Volatile private var ex1L = 0.0
    @Volatile private var ex1T = 0.0
    @Volatile private var ex1R = 0.0
    @Volatile private var ex1B = 0.0
    @Volatile private var ex2L = 0.0
    @Volatile private var ex2T = 0.0
    @Volatile private var ex2R = 0.0
    @Volatile private var ex2B = 0.0
    @Volatile private var callback: ((RawTouch) -> Unit)? = null
    private var attached: SurfaceView? = null
    private var activeZone = 0

    private fun findSurface(v: View?): SurfaceView? {
        if (v is SurfaceView) return v
        if (v is ViewGroup) {
            for (i in 0 until v.childCount) {
                findSurface(v.getChildAt(i))?.let { return it }
            }
        }
        return null
    }

    private fun inRect(x: Double, y: Double, l: Double, t: Double, r: Double, b: Double): Boolean =
        r > l && b > t && x >= l && x <= r && y >= t && y <= b

    private fun zoneAt(x: Double, y: Double): Int {
        if (!capture) return 0
        if (inRect(x, y, r2L, r2T, r2R, r2B)) return 2
        if (inRect(x, y, r1L, r1T, r1R, r1B)) {
            if (inRect(x, y, ex1L, ex1T, ex1R, ex1B)) return 0
            if (inRect(x, y, ex2L, ex2T, ex2R, ex2B)) return 0
            return 1
        }
        return 0
    }

    private val touchExecutor = java.util.concurrent.Executors.newSingleThreadExecutor { r ->
        Thread(r, "kyokai-touch").apply { isDaemon = true }
    }

    private val listener = View.OnTouchListener { v, e ->
        val cb = callback ?: return@OnTouchListener false
        val d = v.resources.displayMetrics.density.toDouble().coerceAtLeast(1.0)
        when (e.actionMasked) {
            MotionEvent.ACTION_DOWN -> {
                activeZone = zoneAt(e.x / d, e.y / d)
                if (activeZone == 0) return@OnTouchListener false
            }
            else -> if (activeZone == 0) return@OnTouchListener false
        }
        val z = activeZone.toLong()
        fun emit(i: Int, phase: String) {
            val kind = when (e.getToolType(i)) {
                MotionEvent.TOOL_TYPE_MOUSE -> "mouse"
                MotionEvent.TOOL_TYPE_STYLUS -> "pen"
                else -> "touch"
            }
            val rt = RawTouch(
                phase = phase,
                kind = kind,
                zone = z,
                id = e.getPointerId(i).toLong(),
                x = e.getX(i) / d,
                y = e.getY(i) / d,
                buttons = e.buttonState.toLong(),
            )
            touchExecutor.execute {
                try {
                    cb(rt)
                } catch (_: Throwable) {
                }
            }
        }
        when (e.actionMasked) {
            MotionEvent.ACTION_DOWN, MotionEvent.ACTION_POINTER_DOWN -> emit(e.actionIndex, "down")
            MotionEvent.ACTION_MOVE -> for (i in 0 until e.pointerCount) emit(i, "move")
            MotionEvent.ACTION_UP, MotionEvent.ACTION_POINTER_UP -> {
                emit(e.actionIndex, "up")
                if (e.actionMasked == MotionEvent.ACTION_UP) activeZone = 0
            }
            MotionEvent.ACTION_CANCEL -> {
                for (i in 0 until e.pointerCount) emit(i, "cancel")
                activeZone = 0
            }
        }
        true
    }

    fun configure(
        cap: Boolean,
        z1L: Double, z1T: Double, z1R: Double, z1B: Double,
        z2L: Double, z2T: Double, z2R: Double, z2B: Double,
        e1L: Double, e1T: Double, e1R: Double, e1B: Double,
        e2L: Double, e2T: Double, e2R: Double, e2B: Double,
        cb: (RawTouch) -> Unit,
    ) {
        val changed = capture != cap
        capture = cap
        r1L = z1L; r1T = z1T; r1R = z1R; r1B = z1B
        r2L = z2L; r2T = z2T; r2R = z2R; r2B = z2B
        ex1L = e1L; ex1T = e1T; ex1R = e1R; ex1B = e1B
        ex2L = e2L; ex2T = e2T; ex2R = e2R; ex2B = e2B
        if (cap) {
            if (callback == null) {
                callback = cb
            }
        } else {
            callback = null
        }
        if (changed || (cap && attached == null)) {
            mainHandler.post {
                val act = Native.activity
                val sv = findSurface(act?.findViewById(android.R.id.content))
                if (!capture) {
                    activeZone = 0
                    attached?.setOnTouchListener(null)
                    attached = null
                } else if (sv != null && sv !== attached) {
                    attached?.setOnTouchListener(null)
                    sv.setOnTouchListener(listener)
                    attached = sv
                }
            }
        }
    }
}

/**
 * Seals and unseals the 32-byte secrets-store master key in
 * noBackupFilesDir/master.key under Android Keystore alias "kyokai-master",
 * compatible with Kyokai-Mobile 0.1.3 so upgrades keep identity and secrets.
 */
private object MasterKey {
    private const val ALIAS = "kyokai-master"
    private const val FILE = "master.key"

    private fun wrapping(): SecretKey {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getKey(ALIAS, null) as? SecretKey)?.let { return it }
        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        gen.init(
            KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build()
        )
        return gen.generateKey()
    }

    fun get(context: Context): String? = try {
        val file = File(context.noBackupFilesDir, FILE)
        val key = wrapping()
        val raw = if (file.exists()) {
            val sealed = file.readBytes()
            Cipher.getInstance("AES/GCM/NoPadding").run {
                init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(128, sealed, 0, 12))
                doFinal(sealed, 12, sealed.size - 12)
            }
        } else {
            val fresh = ByteArray(32).also { SecureRandom().nextBytes(it) }
            val cipher = Cipher.getInstance("AES/GCM/NoPadding").apply { init(Cipher.ENCRYPT_MODE, key) }
            val sealed = cipher.iv + cipher.doFinal(fresh)
            val tmp = File(context.noBackupFilesDir, "$FILE.tmp")
            tmp.writeBytes(sealed)
            if (!tmp.renameTo(file)) throw IllegalStateException("cannot save the secrets key")
            fresh
        }
        Base64.encodeToString(raw, Base64.NO_WRAP).also { raw.fill(0) }
    } catch (_: Exception) {
        null
    }
}

/**
 * Enumerates regular H.264 and HEVC MediaCodec decoders, hardware first,
 * reporting maximum supported frame dimensions.
 */
private object CodecProbe {
    private fun hardwareOf(name: String, info: android.media.MediaCodecInfo): Boolean =
        if (Build.VERSION.SDK_INT >= 29) info.isHardwareAccelerated
        else !name.startsWith("OMX.google.") && !name.startsWith("c2.android.")

    fun decodable(): List<DecoderCap> {
        val infos = MediaCodecList(MediaCodecList.REGULAR_CODECS).codecInfos
        val pairs = listOf("hevc" to MediaFormat.MIMETYPE_VIDEO_HEVC, "h264" to MediaFormat.MIMETYPE_VIDEO_AVC)
        val out = ArrayList<DecoderCap>()
        for ((codec, mime) in pairs) {
            val candidates = infos
                .filter { !it.isEncoder && !it.name.endsWith(".secure") && it.supportedTypes.any { t -> t.equals(mime, true) } }
                .sortedBy { if (hardwareOf(it.name, it)) 0 else 1 }
            val best = candidates.firstOrNull {
                try {
                    MediaCodec.createByCodecName(it.name).release()
                    true
                } catch (_: Exception) {
                    false
                }
            } ?: continue
            val caps = best.getCapabilitiesForType(mime).videoCapabilities
            out.add(
                DecoderCap(
                    codec = codec,
                    name = best.name,
                    hardware = hardwareOf(best.name, best),
                    maxWidth = (caps?.supportedWidths?.upper ?: 0).toLong(),
                    maxHeight = (caps?.supportedHeights?.upper ?: 0).toLong(),
                )
            )
        }
        return out
    }
}

/**
 * Keeps the process alive with a partial wake lock and an ongoing low-importance
 * notification while one or more remote sessions are open.
 */
class SessionService : Service() {
    private var wake: PowerManager.WakeLock? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        try {
            val text = intent?.getStringExtra(EXTRA_TEXT) ?: ""
            val notification = buildNotification(this, text)
            if (Build.VERSION.SDK_INT >= 34) {
                startForeground(ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
            } else {
                startForeground(ID, notification)
            }
            if (wake == null) {
                wake = (getSystemService(Context.POWER_SERVICE) as PowerManager)
                    .newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "kyokai:sessions")
                    .apply { setReferenceCounted(false); acquire() }
            }
        } catch (_: Throwable) {
            try {
                stopSelf(startId)
            } catch (_: Throwable) {
            }
        }
        return START_NOT_STICKY
    }

    override fun onTimeout(startId: Int) {
        try {
            stopSelf(startId)
        } catch (_: Throwable) {
        }
    }

    override fun onTimeout(startId: Int, fgsType: Int) {
        try {
            stopSelf(startId)
        } catch (_: Throwable) {
        }
    }

    override fun onDestroy() {
        try {
            wake?.release()
        } catch (_: Throwable) {
        }
        wake = null
        super.onDestroy()
    }

    companion object {
        private const val ID = 1
        private const val CHANNEL = "sessions"
        private const val EXTRA_TEXT = "text"

        private fun buildNotification(context: Context, text: String): Notification {
            val manager = context.getSystemService(NotificationManager::class.java)
            if (manager.getNotificationChannel(CHANNEL) == null) {
                manager.createNotificationChannel(
                    NotificationChannel(CHANNEL, "Open sessions", NotificationManager.IMPORTANCE_LOW)
                )
            }
            val open = PendingIntent.getActivity(
                context,
                0,
                Intent(context, MyGoActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
                PendingIntent.FLAG_IMMUTABLE,
            )
            return Notification.Builder(context, CHANNEL)
                .setSmallIcon(R.drawable.ic_stat_kyokai)
                .setContentTitle("Kyokai")
                .setContentText(text)
                .setContentIntent(open)
                .setOngoing(true)
                .build()
        }

        fun update(context: Context, text: String?) {
            val intent = Intent(context, SessionService::class.java)
            if (text == null) {
                try {
                    context.stopService(intent)
                } catch (_: Throwable) {
                }
            } else {
                intent.putExtra(EXTRA_TEXT, text)
                try {
                    context.startForegroundService(intent)
                } catch (_: Throwable) {
                }
            }
        }
    }
}

/**
 * Reads and writes PNG images on the Android clipboard, deduplicating by
 * URI and ClipDescription.timestamp so the same image is read once.
 */
private object ClipSync {
    const val AUTHORITY = "com.yuanzhe.kyokai.clip"

    private fun manager(ctx: Context) = ctx.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager

    fun image(ctx: Context, unless: String): ClipImage {
        return try {
            val cm = manager(ctx)
            val clip = cm.primaryClip ?: return ClipImage("", ByteArray(0))
            val desc = clip.description
            if (clip.itemCount == 0 || !(0 until desc.mimeTypeCount).any { desc.getMimeType(it).startsWith("image/") }) {
                return ClipImage("", ByteArray(0))
            }
            val uri = clip.getItemAt(0).uri ?: return ClipImage("", ByteArray(0))
            val at = desc.timestamp
            val mark = "$uri@$at"
            if (mark == unless) return ClipImage(mark, ByteArray(0))
            val bytes = ctx.contentResolver.openInputStream(uri)?.use { it.readBytes() } ?: return ClipImage("", ByteArray(0))
            val type = ctx.contentResolver.getType(uri) ?: ""
            val png = if (type == "image/png") {
                bytes
            } else {
                val bmp = BitmapFactory.decodeByteArray(bytes, 0, bytes.size) ?: return ClipImage("", ByteArray(0))
                ByteArrayOutputStream().also {
                    bmp.compress(Bitmap.CompressFormat.PNG, 100, it)
                    bmp.recycle()
                }.toByteArray()
            }
            ClipImage(mark, png)
        } catch (_: Exception) {
            ClipImage("", ByteArray(0))
        }
    }

    fun setImage(ctx: Context, png: ByteArray): String = try {
        val dir = File(ctx.cacheDir, "clip").apply { mkdirs() }
        dir.listFiles()?.sortedBy { it.lastModified() }?.dropLast(4)?.forEach { it.delete() }
        val file = File(dir, "kyokai-${System.currentTimeMillis()}.png")
        file.writeBytes(png)
        val uri = Uri.parse("content://$AUTHORITY/${file.name}")
        val clip = ClipData.newUri(ctx.contentResolver, "Kyokai", uri)
        manager(ctx).setPrimaryClip(clip)
        val at = manager(ctx).primaryClip?.description?.timestamp ?: 0L
        "$uri@$at"
    } catch (_: Exception) {
        ""
    }
}

/** Serves PNG files placed on the clipboard by [ClipSync.setImage]. */
class ClipProvider : ContentProvider() {
    private fun fileOf(uri: Uri): File? {
        val name = uri.lastPathSegment ?: return null
        if (!name.matches(Regex("kyokai-[0-9]+\\.png"))) return null
        return File(File(context!!.cacheDir, "clip"), name).takeIf { it.isFile }
    }

    override fun onCreate() = true
    override fun getType(uri: Uri) = "image/png"
    override fun openFile(uri: Uri, mode: String): ParcelFileDescriptor? {
        if (mode != "r") throw SecurityException("read only")
        val f = fileOf(uri) ?: throw FileNotFoundException("$uri")
        return ParcelFileDescriptor.open(f, ParcelFileDescriptor.MODE_READ_ONLY)
    }

    override fun query(
        uri: Uri,
        projection: Array<out String>?,
        selection: String?,
        selectionArgs: Array<out String>?,
        sortOrder: String?,
    ): Cursor? {
        val f = fileOf(uri) ?: return null
        val cols = arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE)
        return MatrixCursor(cols).apply { addRow(arrayOf<Any>(f.name, f.length())) }
    }

    override fun insert(uri: Uri, values: ContentValues?) = null
    override fun delete(uri: Uri, selection: String?, selectionArgs: Array<out String>?) = 0
    override fun update(uri: Uri, values: ContentValues?, selection: String?, selectionArgs: Array<out String>?) = 0
}

/**
 * Observes both Sensor.TYPE_HINGE_ANGLE and Jetpack Window's FoldingFeature
 * so foldable laptop mode works across OEMs (Pixel Fold, Galaxy Z Fold, etc.).
 */
private class PostureWatcher(
    private val context: Context,
    private val onChange: (Posture) -> Unit,
) : SensorEventListener {
    private val sensors = context.getSystemService(Context.SENSOR_SERVICE) as SensorManager
    private val hinge: Sensor? = if (Build.VERSION.SDK_INT >= 30) sensors.getDefaultSensor(Sensor.TYPE_HINGE_ANGLE) else null
    private var windowJob: Job? = null

    @Volatile private var angle = -1.0
    @Volatile private var foldHalf = false
    @Volatile private var foldTop = 0L
    @Volatile private var foldBottom = 0L
    @Volatile private var last: Posture? = null

    fun start() {
        hinge?.let { sensors.registerListener(this, it, SensorManager.SENSOR_DELAY_NORMAL) }
        val act = Native.activity
        if (act != null) {
            windowJob = Native.scope.launch(Dispatchers.Main) {
                WindowInfoTracker.getOrCreate(act).windowLayoutInfo(act).collect { info ->
                    val feat = info.displayFeatures.filterIsInstance<FoldingFeature>()
                        .firstOrNull { it.orientation == FoldingFeature.Orientation.HORIZONTAL }
                    if (feat != null && feat.state == FoldingFeature.State.HALF_OPENED) {
                        foldHalf = true
                        foldTop = feat.bounds.top.toLong()
                        foldBottom = feat.bounds.bottom.toLong()
                    } else {
                        foldHalf = false
                        foldTop = 0L
                        foldBottom = 0L
                    }
                    emit()
                }
            }
        }
    }

    fun stop() {
        sensors.unregisterListener(this)
        windowJob?.cancel()
        windowJob = null
    }

    override fun onSensorChanged(event: SensorEvent) {
        angle = event.values[0].toDouble()
        emit()
    }

    override fun onAccuracyChanged(sensor: Sensor?, accuracy: Int) {}

    private fun emit() {
        val sensorHalf = angle in 30.0..150.0
        val p = Posture(
            halfOpened = foldHalf || sensorHalf,
            hingeTop = foldTop,
            hingeBottom = foldBottom,
            angle = angle,
        )
        val prev = last
        val changed = prev == null ||
            prev.halfOpened != p.halfOpened ||
            prev.hingeTop != p.hingeTop ||
            prev.hingeBottom != p.hingeBottom ||
            kotlin.math.abs(prev.angle - p.angle) >= 3.0
        if (changed) {
            last = p
            onChange(p)
        }
    }
}

/**
 * Decodes 48 kHz stereo 10 ms Opus packets via Android MediaCodec and plays
 * them on a low-latency AudioTrack, dropping queued packets older than 200 ms.
 */
private class OpusSound private constructor() : SoundPlayer {
    private val queue = LinkedBlockingDeque<ByteArray>()
    @Volatile private var running = true
    private val thread = Thread({ loop() }, "kyokai-sound")

    init {
        thread.start()
    }

    override fun feed(packet: ByteArray) {
        queue.offer(packet.copyOf())
        while (queue.size > 20) queue.poll()
    }

    override fun close() {
        running = false
        thread.interrupt()
    }

    private fun openCodec(): MediaCodec {
        val format = MediaFormat.createAudioFormat(MediaFormat.MIMETYPE_AUDIO_OPUS, RATE, CHANNELS)
        format.setByteBuffer("csd-0", ByteBuffer.wrap(opusHead()))
        format.setByteBuffer("csd-1", ByteBuffer.allocate(8).order(ByteOrder.nativeOrder()).putLong(0, 0L))
        format.setByteBuffer("csd-2", ByteBuffer.allocate(8).order(ByteOrder.nativeOrder()).putLong(0, 80_000_000L))
        return MediaCodec.createDecoderByType(MediaFormat.MIMETYPE_AUDIO_OPUS).apply {
            configure(format, null, null, 0)
            start()
        }
    }

    private fun openTrack(): AudioTrack {
        val min = AudioTrack.getMinBufferSize(RATE, AudioFormat.CHANNEL_OUT_STEREO, AudioFormat.ENCODING_PCM_16BIT)
        return AudioTrack.Builder()
            .setAudioAttributes(
                AudioAttributes.Builder()
                    .setUsage(AudioAttributes.USAGE_MEDIA)
                    .setContentType(AudioAttributes.CONTENT_TYPE_MUSIC)
                    .build()
            )
            .setAudioFormat(
                AudioFormat.Builder()
                    .setSampleRate(RATE)
                    .setChannelMask(AudioFormat.CHANNEL_OUT_STEREO)
                    .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                    .build()
            )
            .setBufferSizeInBytes(maxOf(min, RATE * 4 / 10))
            .setTransferMode(AudioTrack.MODE_STREAM)
            .setPerformanceMode(AudioTrack.PERFORMANCE_MODE_LOW_LATENCY)
            .build()
            .apply { play() }
    }

    private fun loop() {
        var codec: MediaCodec? = null
        var track: AudioTrack? = null
        var pts = 0L
        val info = MediaCodec.BufferInfo()
        while (running) {
            try {
                if (codec == null) codec = openCodec()
                if (track == null) track = openTrack()
                val c = codec
                val packet = queue.poll(20, TimeUnit.MILLISECONDS)
                if (packet != null) {
                    val index = c.dequeueInputBuffer(10_000)
                    if (index >= 0) {
                        val input = c.getInputBuffer(index)
                        if (input != null && input.capacity() >= packet.size) {
                            input.clear()
                            input.put(packet)
                            c.queueInputBuffer(index, 0, packet.size, pts, 0)
                            pts += 10_000
                        } else if (input != null) {
                            c.queueInputBuffer(index, 0, 0, 0, 0)
                        }
                    }
                }
                while (true) {
                    val out = c.dequeueOutputBuffer(info, 0)
                    if (out < 0) break
                    val pcm = c.getOutputBuffer(out)
                    if (pcm != null && info.size > 0) {
                        pcm.position(info.offset)
                        pcm.limit(info.offset + info.size)
                        track.write(pcm, info.size, AudioTrack.WRITE_BLOCKING)
                    }
                    c.releaseOutputBuffer(out, false)
                }
            } catch (_: InterruptedException) {
                break
            } catch (_: Exception) {
                try { codec?.release() } catch (_: Exception) {}
                codec = null
                try { Thread.sleep(200) } catch (_: InterruptedException) { break }
            }
        }
        try { codec?.stop() } catch (_: Exception) {}
        try { codec?.release() } catch (_: Exception) {}
        try { track?.stop() } catch (_: Exception) {}
        try { track?.release() } catch (_: Exception) {}
    }

    companion object {
        private const val RATE = 48_000
        private const val CHANNELS = 2

        private fun opusHead(): ByteArray {
            val b = ByteBuffer.allocate(19).order(ByteOrder.LITTLE_ENDIAN)
            b.put("OpusHead".toByteArray(Charsets.US_ASCII))
            b.put(1)
            b.put(CHANNELS.toByte())
            b.putShort(0)
            b.putInt(RATE)
            b.putShort(0)
            b.put(0)
            return b.array()
        }

        fun open(): OpusSound? = try { OpusSound() } catch (_: Exception) { null }
    }
}
