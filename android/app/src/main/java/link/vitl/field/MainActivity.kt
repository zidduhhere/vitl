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
import androidx.core.view.WindowCompat
import java.io.ByteArrayOutputStream
import java.io.File
import java.net.HttpURLConnection
import java.net.URL
import java.util.concurrent.Executors
import kotlin.math.abs
import org.json.JSONObject

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
    private lateinit var etPort: EditText
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
    private lateinit var btnAudio: Button
    private lateinit var tvMediaProgress: TextView

    private lateinit var btnEndSession: Button

    // ---- Device Emulator ----
    private lateinit var etDeviceServer: EditText
    private lateinit var etDevicePort: EditText
    private lateinit var btnConnectDevice: Button
    @Volatile private var isPollingDevice = false
    private var lastPolledVitals: JSONObject? = null

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
                photoUri?.let { showImageQualityDialog(it) }
            } else {
                runOnUiThread { tvMediaProgress.text = "Camera cancelled" }
            }
        }

    private fun showImageQualityDialog(uri: Uri) {
        val options = arrayOf("Fast (Low Res)", "Detailed (High Res)")
        android.app.AlertDialog.Builder(this)
            .setTitle("Select Image Quality")
            .setItems(options) { _, which ->
                val isDetailed = which == 1
                sendCapturedImage(uri, isDetailed)
            }
            .setCancelable(false)
            .show()
    }

    private val requestCameraPermission =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
            if (granted) launchCamera()
            else runOnUiThread { tvMediaProgress.text = "Camera permission denied" }
        }

    // ---- Lifecycle ----

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WindowCompat.getInsetsController(window, window.decorView).isAppearanceLightStatusBars = true
        setContentView(R.layout.activity_main)

        workerId = intent.getLongExtra("WORKER_ID", 1L)

        etServer = findViewById(R.id.etServer)
        etPort = findViewById(R.id.etPort)
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
        btnAudio = findViewById(R.id.btnAudio)
        tvMediaProgress = findViewById(R.id.tvMediaProgress)

        btnEndSession = findViewById(R.id.btnEndSession)

        etDeviceServer = findViewById(R.id.etDeviceServer)
        etDevicePort = findViewById(R.id.etDevicePort)
        btnConnectDevice = findViewById(R.id.btnConnectDevice)
        val btnAddPatient: Button = findViewById(R.id.btnAddPatient)

        setSessionControlsEnabled(false)

        btnStart.setOnClickListener { startSession() }
        btnAddPatient.setOnClickListener { showAddPatientDialog() }
        btnSendVitals.setOnClickListener { sendVitals() }
        btnCapture.setOnClickListener { checkCameraAndCapture() }
        btnEndSession.setOnClickListener { endSession() }
        
        btnConnectDevice.setOnClickListener { toggleDeviceConnection() }
        
        btnAudio.setOnClickListener {
            toast("Codec feature yet to come")
        }

        // Setup + and - buttons
        setupStepper(findViewById(R.id.btnHrMinus), findViewById(R.id.btnHrPlus), etHR, 1, 0, 300)
        setupStepper(findViewById(R.id.btnSpo2Minus), findViewById(R.id.btnSpo2Plus), etSpO2, 1, 0, 100)
        setupStepper(findViewById(R.id.btnSysMinus), findViewById(R.id.btnSysPlus), etBPSys, 1, 0, 300)
        setupStepper(findViewById(R.id.btnDiaMinus), findViewById(R.id.btnDiaPlus), etBPDia, 1, 0, 200)
        
        // Temp handles decimals
        findViewById<Button>(R.id.btnTempMinus).setOnClickListener {
            val current = etTemp.text.toString().toDoubleOrNull() ?: 36.8
            etTemp.setText(String.format(java.util.Locale.US, "%.1f", current - 0.1))
        }
        findViewById<Button>(R.id.btnTempPlus).setOnClickListener {
            val current = etTemp.text.toString().toDoubleOrNull() ?: 36.8
            etTemp.setText(String.format(java.util.Locale.US, "%.1f", current + 0.1))
        }
    }

    private fun setupStepper(btnMinus: Button, btnPlus: Button, et: EditText, step: Int, min: Int, max: Int) {
        btnMinus.setOnClickListener {
            val current = et.text.toString().toIntOrNull() ?: return@setOnClickListener
            if (current - step >= min) et.setText((current - step).toString())
        }
        btnPlus.setOnClickListener {
            val current = et.text.toString().toIntOrNull() ?: return@setOnClickListener
            if (current + step <= max) et.setText((current + step).toString())
        }
    }

    private fun showAddPatientDialog() {
        val view = layoutInflater.inflate(R.layout.dialog_add_patient, null)
        val dialog = android.app.AlertDialog.Builder(this)
            .setView(view)
            .create()

        val etName: EditText = view.findViewById(R.id.etPatientName)
        val etAge: EditText = view.findViewById(R.id.etPatientAge)
        val etSex: EditText = view.findViewById(R.id.etPatientSex)
        val etConditions: EditText = view.findViewById(R.id.etPatientConditions)
        val etAllergies: EditText = view.findViewById(R.id.etPatientAllergies)
        val etMedications: EditText = view.findViewById(R.id.etPatientMedications)
        val etNotes: EditText = view.findViewById(R.id.etPatientNotes)
        val btnSave: Button = view.findViewById(R.id.btnSavePatient)

        btnSave.setOnClickListener {
            val name = etName.text.toString().trim()
            val age = etAge.text.toString().toIntOrNull() ?: 0
            val sex = etSex.text.toString().trim()
            val conditions = etConditions.text.toString().trim().split(",").map { it.trim() }.filter { it.isNotEmpty() }
            val allergies = etAllergies.text.toString().trim().split(",").map { it.trim() }.filter { it.isNotEmpty() }
            val medications = etMedications.text.toString().trim().split(",").map { it.trim() }.filter { it.isNotEmpty() }
            val notes = etNotes.text.toString().trim()

            if (name.isEmpty()) {
                toast("Name is required")
                return@setOnClickListener
            }

            val payload = JSONObject().apply {
                put("Name", name)
                put("Age", age)
                put("Sex", sex)
                val jsonConditions = org.json.JSONArray()
                conditions.forEach { jsonConditions.put(it) }
                put("Conditions", jsonConditions)
                
                val jsonAllergies = org.json.JSONArray()
                allergies.forEach { jsonAllergies.put(it) }
                put("Allergies", jsonAllergies)
                
                val jsonMedications = org.json.JSONArray()
                medications.forEach { jsonMedications.put(it) }
                put("Medications", jsonMedications)
                
                put("LastVisitNotes", notes)
            }

            val serverIp = etServer.text.toString()
            val urlString = "http://$serverIp:8080/api/patients"

            btnSave.isEnabled = false
            btnSave.text = "Saving..."

            executor.execute {
                try {
                    val url = URL(urlString)
                    val conn = url.openConnection() as HttpURLConnection
                    conn.requestMethod = "POST"
                    conn.setRequestProperty("Content-Type", "application/json")
                    conn.doOutput = true
                    
                    val outputBytes = payload.toString().toByteArray(Charsets.UTF_8)
                    conn.outputStream.write(outputBytes)
                    conn.outputStream.flush()
                    
                    val responseCode = conn.responseCode
                    if (responseCode == 200) {
                        val response = conn.inputStream.bufferedReader().use { it.readText() }
                        val json = JSONObject(response)
                        val newId = json.getString("patient_id")
                        
                        runOnUiThread {
                            etPatientID.setText(newId)
                            toast("Patient Added! ID: $newId")
                            dialog.dismiss()
                        }
                    } else {
                        runOnUiThread {
                            toast("Failed to save patient. Code: $responseCode")
                            btnSave.isEnabled = true
                            btnSave.text = "Save Patient"
                        }
                    }
                    conn.disconnect()
                } catch (e: Exception) {
                    android.util.Log.e("AddPatient", "Error", e)
                    runOnUiThread {
                        toast("Error connecting to server")
                        btnSave.isEnabled = true
                        btnSave.text = "Save Patient"
                    }
                }
            }
        }
        
        dialog.show()
    }

    override fun onDestroy() {
        super.onDestroy()
        client?.close()
        executor.shutdown()
    }

    // ---- Session ----

    private fun startSession() {
        val serverIp = etServer.text.toString().trim()
        val serverPort = etPort.text.toString().trim()
        val serverAddr = "$serverIp:$serverPort"
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

    // ---- Device Polling ----

    private fun toggleDeviceConnection() {
        if (isPollingDevice) {
            isPollingDevice = false
            btnConnectDevice.text = "Sync Vitals from Device"
            btnConnectDevice.setTextColor(ContextCompat.getColor(this, R.color.ink))
            setVitalsInputEnabled(client != null)
            toast("Stopped syncing from device")
        } else {
            val deviceIp = etDeviceServer.text.toString().trim()
            val devicePort = etDevicePort.text.toString().trim()
            if (deviceIp.isEmpty() || devicePort.isEmpty()) {
                toast("Enter valid device IP/Port")
                return
            }
            
            isPollingDevice = true
            btnConnectDevice.text = "Syncing..."
            btnConnectDevice.setTextColor(ContextCompat.getColor(this, R.color.signal_teal))
            lastPolledVitals = null
            setVitalsInputEnabled(false)
            
            executor.submit { pollDevice(deviceIp, devicePort) }
        }
    }

    private fun pollDevice(ip: String, port: String) {
        val url = URL("http://$ip:$port/api/emulator/vitals")
        while (isPollingDevice) {
            try {
                val conn = url.openConnection() as HttpURLConnection
                conn.requestMethod = "GET"
                conn.connectTimeout = 2000
                conn.readTimeout = 0 // Wait indefinitely for streaming data

                if (conn.responseCode == 200) {
                    val reader = conn.inputStream.bufferedReader()
                    while (isPollingDevice) {
                        val line = reader.readLine()
                        if (line == null) break
                        if (line.startsWith("data: ")) {
                            val jsonStr = line.substring(6).trim()
                            if (jsonStr.isNotEmpty()) {
                                val json = JSONObject(jsonStr)
                                handlePolledVitals(json)
                            }
                        }
                    }
                }
                conn.disconnect()
            } catch (e: Exception) {
                android.util.Log.e("pollDevice", "Error polling device", e)
                runOnUiThread { toast("Polling error: ${e.message}") }
            }
            if (isPollingDevice) {
                Thread.sleep(2000)
            }
        }
    }
    
    private fun handlePolledVitals(json: JSONObject) {
        if (!json.has("heartRate")) return
        
        val hr = json.getInt("heartRate")
        val spo2 = json.getInt("spO2")
        val sys = json.getInt("bpSystolic")
        val dia = json.getInt("bpDiastolic")
        val temp = json.getDouble("tempC")

        var shouldPush = false
        
        val last = lastPolledVitals
        if (last == null) {
            shouldPush = true // First time, always push
        } else {
            if (abs(hr - last.getInt("heartRate")) >= 5) shouldPush = true
            if (abs(spo2 - last.getInt("spO2")) >= 2) shouldPush = true
            if (abs(sys - last.getInt("bpSystolic")) >= 5) shouldPush = true
            if (abs(dia - last.getInt("bpDiastolic")) >= 5) shouldPush = true
            if (abs(temp - last.getDouble("tempC")) >= 0.5) shouldPush = true
        }

        lastPolledVitals = json

        runOnUiThread {
            etHR.setText(hr.toString())
            etSpO2.setText(spo2.toString())
            etBPSys.setText(sys.toString())
            etBPDia.setText(dia.toString())
            etTemp.setText(String.format(java.util.Locale.US, "%.1f", temp))
            
            if (shouldPush && client != null) {
                toast("Noticeable change detected. Pushing vitals...")
                sendVitals()
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

    private fun sendCapturedImage(uri: Uri, isDetailed: Boolean) {
        tvMediaProgress.text = "Encoding image…"
        val c = client ?: return toast("No active session")

        executor.submit {
            try {
                // Decode, downscale and compress based on selected quality
                val bmp = if (android.os.Build.VERSION.SDK_INT >= android.os.Build.VERSION_CODES.P) {
                    val source = android.graphics.ImageDecoder.createSource(contentResolver, uri)
                    android.graphics.ImageDecoder.decodeBitmap(source)
                } else {
                    @Suppress("DEPRECATION")
                    MediaStore.Images.Media.getBitmap(contentResolver, uri)
                }
                
                val maxPx = if (isDetailed) 1024 else 320
                val quality = if (isDetailed) 80 else 40
                
                val jpegBytes = downscaleAndCompress(bmp, maxPx, quality)
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
                "patient_locked" -> {
                    tvStatus.text = "✗ Another worker already has an active session for this patient"
                    btnStart.isEnabled = true
                }
                "unauthorized" -> {
                    tvStatus.text = "✗ Device not authorized (check pre-shared key)"
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
        
        setVitalsInputEnabled(enabled && !isPollingDevice)
    }

    private fun setVitalsInputEnabled(enabled: Boolean) {
        etHR.isEnabled = enabled
        etSpO2.isEnabled = enabled
        etBPSys.isEnabled = enabled
        etBPDia.isEnabled = enabled
        etTemp.isEnabled = enabled
        
        findViewById<Button>(R.id.btnHrMinus).isEnabled = enabled
        findViewById<Button>(R.id.btnHrPlus).isEnabled = enabled
        findViewById<Button>(R.id.btnSpo2Minus).isEnabled = enabled
        findViewById<Button>(R.id.btnSpo2Plus).isEnabled = enabled
        findViewById<Button>(R.id.btnSysMinus).isEnabled = enabled
        findViewById<Button>(R.id.btnSysPlus).isEnabled = enabled
        findViewById<Button>(R.id.btnDiaMinus).isEnabled = enabled
        findViewById<Button>(R.id.btnDiaPlus).isEnabled = enabled
        findViewById<Button>(R.id.btnTempMinus).isEnabled = enabled
        findViewById<Button>(R.id.btnTempPlus).isEnabled = enabled
    }

    private fun toast(msg: String) =
        Toast.makeText(this, msg, Toast.LENGTH_SHORT).show()
}
