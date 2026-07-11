// VitalLink Dashboard — Configuration
// Edit WS_URL to match your server's address/port.
// The server default is ws://localhost:8080/ws (flag --ws :8080).

const CONFIG = {
  WS_URL: "ws://localhost:8080/ws",
  DOCTOR_ID: 1,                  // Identifies this doctor client (uint16)
  VITALS_CHART_WINDOW: 60,       // Number of readings to keep on the live chart
  STALE_THRESHOLD_MS: 10000,     // ms before vitals are shown as "stale"
};
