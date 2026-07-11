package link.vitl.field

import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import androidx.core.view.WindowCompat

class LoginActivity : AppCompatActivity() {

    private lateinit var tvPinPrompt: TextView
    private lateinit var tvPinDisplay: TextView
    private lateinit var etWorkerID: EditText
    
    private var currentInput = ""
    private var savedPin: String? = null
    
    private val PREFS_NAME = "VitlPrefs"
    private val PREF_PIN = "worker_pin"
    private val PREF_WORKER_ID = "worker_id"

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        WindowCompat.getInsetsController(window, window.decorView).isAppearanceLightStatusBars = true
        setContentView(R.layout.activity_login)

        tvPinPrompt = findViewById(R.id.tvPinPrompt)
        tvPinDisplay = findViewById(R.id.tvPinDisplay)
        etWorkerID = findViewById(R.id.etWorkerID)

        val prefs = getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
        savedPin = prefs.getString(PREF_PIN, null)
        val savedWorkerID = prefs.getLong(PREF_WORKER_ID, 1L)
        etWorkerID.setText(savedWorkerID.toString())

        if (savedPin == null) {
            tvPinPrompt.text = "SET NEW PIN"
            tvPinPrompt.setTextColor(ContextCompat.getColor(this, R.color.alert_red))
        } else {
            tvPinPrompt.text = "ENTER PIN"
        }

        setupNumpad()
    }

    private fun setupNumpad() {
        val buttons = listOf(
            R.id.btn0, R.id.btn1, R.id.btn2, R.id.btn3, R.id.btn4, 
            R.id.btn5, R.id.btn6, R.id.btn7, R.id.btn8, R.id.btn9
        )

        buttons.forEachIndexed { index, id ->
            findViewById<Button>(id).setOnClickListener { appendPin(index.toString()) }
        }

        findViewById<Button>(R.id.btnClear).setOnClickListener {
            currentInput = ""
            updatePinDisplay()
        }

        findViewById<Button>(R.id.btnEnter).setOnClickListener {
            submitPin()
        }
    }

    private fun appendPin(digit: String) {
        if (currentInput.length < 4) {
            currentInput += digit
            updatePinDisplay()
        }
    }

    private fun updatePinDisplay() {
        // e.g. input "12", pad to 4 -> "1 2 _ _"
        val chars = currentInput.padEnd(4, '_').toCharArray()
        tvPinDisplay.text = chars.joinToString(" ")
    }

    private fun submitPin() {
        if (currentInput.length < 4) {
            Toast.makeText(this, "Enter 4 digits", Toast.LENGTH_SHORT).show()
            return
        }

        val workerIdStr = etWorkerID.text.toString()
        if (workerIdStr.isEmpty()) {
            Toast.makeText(this, "Enter Worker ID", Toast.LENGTH_SHORT).show()
            return
        }
        val workerId = workerIdStr.toLongOrNull() ?: 1L

        val prefs = getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)

        if (savedPin == null) {
            // First time setup
            prefs.edit()
                .putString(PREF_PIN, currentInput)
                .putLong(PREF_WORKER_ID, workerId)
                .apply()
            savedPin = currentInput
            proceedToMain(workerId)
        } else {
            // Verify
            if (currentInput == savedPin) {
                prefs.edit().putLong(PREF_WORKER_ID, workerId).apply()
                proceedToMain(workerId)
            } else {
                Toast.makeText(this, "ACCESS DENIED", Toast.LENGTH_SHORT).show()
                currentInput = ""
                updatePinDisplay()
            }
        }
    }

    private fun proceedToMain(workerId: Long) {
        val intent = Intent(this, MainActivity::class.java)
        intent.putExtra("WORKER_ID", workerId)
        startActivity(intent)
        finish()
    }
}
