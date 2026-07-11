package link.vitl.field

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Bitmap
import android.net.Uri
import android.os.Bundle
import android.provider.MediaStore
import android.widget.*
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.content.FileProvider
import java.io.ByteArrayOutputStream
import java.io.File
import java.util.concurrent.Executors

// gomobile-generated package — produced by `mobile/build_android.sh`.
// The package name is derived from the Go package path last component ("vitallink").
import vitallink.Client
import vitallink.Listener
import vitallink.Vitallink

/**
 * Single Activity for the VitalLink field-worker app.
 *
 * Layout (activity_main.xml):
 *   - Server address, Worker ID, Patient ID fields + "Start Session" button
 *   - Status text view
 *   - Vitals entry fields (HR, SpO2, BP systolic/diastolic, temp) + "Send Vitals" button
 *   - "Capture & Send Image" button + progress text
 *   - "End Session" button
 *
 * Threading:
 *   All Listener callbacks come from the Go receive-loop goroutine (not main thread).
 *   We dispatch to main thread via runOnUiThread before touching any View.
 */
class MainActivity : AppCompatActivity(), Listener {

    // ---- UI refs ----
    private lateinit var etServer: EditText
    private lateinit var etPatientID: EditText
    private lateinit var btnStart: Button

    private lateinit var tvStatus: TextView

    private lateinit var etHR: EditText
    private lateinit var etSpO2: EditText
    private lateinit var etBPSys: EditText
    private lateinit var etBPDia: EditText
    private lateinit var etTemp: EditText
    private lateinit var btnSendVitals: Button

    private lateinit var btnCapture: Button
    private lateinit var tvMediaProgress: TextView

    private lateinit var btnEndSession: Button

    // ---- VitalLink client ----
    private var client: Client? = null
    private val executor = Executors.newCachedThreadPool()

    private var workerId: Long = 1L

    // Tracks the current vitals sequence for display only
    private var lastVitalsSeq = -1

    // ---- Camera capture ----
    private var photoUri: Uri? = null

    private val takePicture =
        registerForActivityResult(ActivityResultContracts.TakePicture()) { success ->
            if (success) {
                photoUri?.let { sendCapturedImage(it) }
            } else {
                runOnUiThread { tvMediaProgress.text = "Camera cancelled" }
            }
        }

    private val requestCameraPermission =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            if (granted) launchCamera()
            else runOnUiThread { tvMediaProgress.text = "Camera permission denied" }
        }

    // ---- Lifecycle ----

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)

        workerId = intent.getLongExtra("WORKER_ID", 1L)

        etServer = findViewById(R.id.etServer)
        etPatientID = findViewById(R.id.etPatientID)
        btnStart = findViewById(R.id.btnStart)

        tvStatus = findViewById(R.id.tvStatus)

        etHR = findViewById(R.id.etHR)
        etSpO2 = findViewById(R.id.etSpO2)
        etBPSys = findViewById(R.id.etBPSys)
        etBPDia = findViewById(R.id.etBPDia)
        etTemp = findViewById(R.id.etTemp)
        btnSendVitals = findViewById(R.id.btnSendVitals)

        btnCapture = findViewById(R.id.btnCapture)
        tvMediaProgress = findViewById(R.id.tvMediaProgress)

        btnEndSession = findViewById(R.id.btnEndSession)

        setSessionControlsEnabled(false)

        btnStart.setOnClickListener { startSession() }
        btnSendVitals.setOnClickListener { sendVitals() }
        btnCapture.setOnClickListener { checkCameraAndCapture() }
        btnEndSession.setOnClickListener { endSession() }
    }

    override fun onDestroy() {
        super.onDestroy()
        client?.close()
        executor.shutdown()
    }

    // ---- Session ----

    private fun startSession() {
        val serverAddr = etServer.text.toString().trim()
        val patientID = etPatientID.text.toString().toLongOrNull() ?: 1001L

        tvStatus.text = "Connecting to $serverAddr …"
        btnStart.isEnabled = false

        executor.submit {
            try {
                val c = Vitallink.newClient(serverAddr, this@MainActivity)
                client = c
                c.startSession(workerId, patientID)
                // OnSessionStatus callback will update UI
            } catch (e: Exception) {
                runOnUiThread {
                    tvStatus.text = "Error: ${e.message}"
                    btnStart.isEnabled = true
                }
            }
        }
    }

    private fun endSession() {
        executor.submit {
            try {
                client?.endSession()
            } catch (_: Exception) { }
            runOnUiThread {
                tvStatus.text = "Session ended"
                setSessionControlsEnabled(false)
                btnStart.isEnabled = true
                client = null
            }
        }
    }

    // ---- Vitals ----

    private fun sendVitals() {
        val hr = etHR.text.toString().toIntOrNull() ?: return toast("Enter heart rate")
        val spo2 = etSpO2.text.toString().toIntOrNull() ?: return toast("Enter SpO2")
        val sys = etBPSys.text.toString().toIntOrNull() ?: return toast("Enter BP systolic")
        val dia = etBPDia.text.toString().toIntOrNull() ?: return toast("Enter BP diastolic")
        val tempStr = etTemp.text.toString().trim()
        // Accept "36.8" or "368" — normalise to x10 integer
        val tempX10 = if (tempStr.contains('.')) {
            (tempStr.toDoubleOrNull() ?: 36.8) * 10
        } else {
            tempStr.toDoubleOrNull() ?: 368.0
        }.toInt()

        val c = client ?: return toast("No active session")
        executor.submit {
            try {
                c.sendVitals(hr.toLong(), spo2.toLong(), sys.toLong(), dia.toLong(), tempX10.toLong())
                // OnVitalsAck callback handles the UI update
            } catch (e: Exception) {
                runOnUiThread { tvStatus.text = "Vitals error: ${e.message}" }
            }
        }
    }

    // ---- Image capture ----

    private fun checkCameraAndCapture() {
        if (ContextCompat.checkSelfPermission(this, Manifest.permission.CAMERA)
            == PackageManager.PERMISSION_GRANTED
        ) {
            launchCamera()
        } else {
            requestCameraPermission.launch(Manifest.permission.CAMERA)
        }
    }

    private fun launchCamera() {
        val file = File(cacheDir, "vitl_capture.jpg")
        val uri = FileProvider.getUriForFile(this, "$packageName.fileprovider", file)
        photoUri = uri
        takePicture.launch(uri)
    }

    private fun sendCapturedImage(uri: Uri) {
        tvMediaProgress.text = "Encoding image…"
        val c = client ?: return toast("No active session")

        executor.submit {
            try {
                // Decode, downscale to ≤320px long-side, JPEG-compress at quality 40.
                val bmp = if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.P) {
                    val source = android.graphics.ImageDecoder.createSource(contentResolver, uri)
                    android.graphics.ImageDecoder.decodeBitmap(source)
                } else {
                    @Suppress("DEPRECATION")
                    MediaStore.Images.Media.getBitmap(contentResolver, uri)
                }
                val jpegBytes = downscaleAndCompress(bmp)
                runOnUiThread { tvMediaProgress.text = "Sending ${jpegBytes.size} bytes…" }
                c.sendImage(jpegBytes)
                // OnMediaProgress callback handles updates
            } catch (e: Exception) {
                runOnUiThread { tvMediaProgress.text = "Image error: ${e.message}" }
            }
        }
    }

    /** Downscale bitmap so the long side is at most maxPx, then JPEG-compress. */
    private fun downscaleAndCompress(bmp: Bitmap, maxPx: Int = 320, quality: Int = 40): ByteArray {
        val (w, h) = bmp.width to bmp.height
        val scale = minOf(maxPx.toFloat() / w, maxPx.toFloat() / h, 1f)
        val scaled = if (scale < 1f) {
            Bitmap.createScaledBitmap(bmp, (w * scale).toInt(), (h * scale).toInt(), true)
        } else bmp
        val baos = ByteArrayOutputStream()
        scaled.compress(Bitmap.CompressFormat.JPEG, quality, baos)
        return baos.toByteArray()
    }

    // ---- Listener callbacks (called from Go goroutine — not main thread) ----

    override fun onSessionStatus(status: String, sessionToken: Long) {
        runOnUiThread {
            when (status) {
                "ok" -> {
                    tvStatus.text = "✓ Session $sessionToken established (doctor connected)"
                    setSessionControlsEnabled(true)
                }
                "no_doctor" -> {
                    tvStatus.text = "✓ Session $sessionToken — doctor connecting…"
                    setSessionControlsEnabled(true)
                }
                "patient_not_found" -> {
                    tvStatus.text = "✗ Patient not found on server"
                    btnStart.isEnabled = true
                }
                else -> {
                    tvStatus.text = "✗ $status"
                    btnStart.isEnabled = true
                }
            }
        }
    }

    override fun onDoctorReady(doctorID: Long, message: String) {
        runOnUiThread {
            tvStatus.text = "Doctor #$doctorID ready: \"$message\""
        }
    }

    override fun onDoctorMsg(code: Long) {
        val label = when (code.toInt()) {
            0x01 -> "Stand by"
            0x02 -> "Administer O₂"
            0x03 -> "Evacuate now"
            0x04 -> "Continue monitoring"
            else -> "Instruction code 0x${code.toString(16)}"
        }
        runOnUiThread {
            Toast.makeText(this, "Doctor: $label", Toast.LENGTH_LONG).show()
            tvStatus.text = "Doctor instruction: $label"
        }
    }

    override fun onVitalsAck(seq: Long, ok: Boolean) {
        lastVitalsSeq = seq.toInt()
        runOnUiThread {
            tvStatus.text = if (ok) "✓ Vitals seq=$seq ACKed" else "⚠ Vitals seq=$seq unACKed (dropped)"
        }
    }

    override fun onMediaProgress(mediaID: Long, sent: Long, total: Long) {
        runOnUiThread {
            tvMediaProgress.text = if (sent >= total && total > 0) {
                "✓ Image transfer #$mediaID complete ($total chunks)"
            } else {
                "Image #$mediaID: $sent/$total chunks…"
            }
        }
    }

    // ---- Helpers ----

    private fun setSessionControlsEnabled(enabled: Boolean) {
        btnSendVitals.isEnabled = enabled
        btnCapture.isEnabled = enabled
        btnEndSession.isEnabled = enabled
        etHR.isEnabled = enabled
        etSpO2.isEnabled = enabled
        etBPSys.isEnabled = enabled
        etBPDia.isEnabled = enabled
        etTemp.isEnabled = enabled
    }

    private fun toast(msg: String) =
        Toast.makeText(this, msg, Toast.LENGTH_SHORT).show()
}
