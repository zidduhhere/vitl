package main

// JSON message shapes exchanged with the doctor dashboard over WebSocket.
// This link is unconstrained (normal clinic connectivity), so plain JSON
// is fine — no need for the compact binary format used on the field link.

type demographics struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
	Sex  string `json:"sex"`
}

// wsEHRPush is sent server -> doctor once a session's patient resolves.
type wsEHRPush struct {
	Type            string       `json:"type"` // "ehr_push"
	SessionToken    string       `json:"session_token"`
	PatientID       string       `json:"patient_id"`
	Demographics    demographics `json:"demographics"`
	KnownConditions []string     `json:"known_conditions"`
	Allergies       []string     `json:"allergies"`
	Medications     []string     `json:"medications"`
	LastVisitNotes  string       `json:"last_visit_notes"`
	WorkerID        string       `json:"worker_id"`
}

// wsVitals is sent server -> doctor on every fresh VITALS packet.
type wsVitals struct {
	Type         string  `json:"type"` // "vitals"
	SessionToken string  `json:"session_token"`
	SeqNum       uint16  `json:"seq_num"`
	HeartRate    byte    `json:"heart_rate"`
	SpO2         byte    `json:"spo2"`
	BPSystolic   byte    `json:"bp_systolic"`
	BPDiastolic  byte    `json:"bp_diastolic"`
	TempC        float64 `json:"temp_c"`
	DeltaFlag    byte    `json:"delta_flag"`
	Timestamp    uint32  `json:"timestamp"`
}

// wsMedia is sent server -> doctor once a chunked transfer fully reassembles.
type wsMedia struct {
	Type         string `json:"type"` // "media"
	SessionToken string `json:"session_token"`
	MediaID      uint16 `json:"media_id"`
	Kind         string `json:"kind"` // "audio" | "image"
	DataBase64   string `json:"data_base64"`
}

// wsMediaStatus informs the doctor dashboard a media transfer was
// abandoned after exhausting its NACK retry budget, rather than leaving the
// doctor waiting on a transfer that will never complete.
type wsMediaStatus struct {
	Type         string `json:"type"` // "media_failed"
	SessionToken string `json:"session_token"`
	MediaID      uint16 `json:"media_id"`
	Kind         string `json:"kind"` // "audio" | "image"
	Reason       string `json:"reason"`
}

// wsSessionStatus informs the doctor dashboard a session started/ended.
type wsSessionStatus struct {
	Type         string `json:"type"` // "session_status"
	SessionToken string `json:"session_token"`
	Status       string `json:"status"` // "active" | "ended"
	// Queued is true when this session started with no doctor dashboard
	// connected — it was queued and is being announced now that one is.
	Queued bool `json:"queued,omitempty"`
}

// wsIncoming is received doctor -> server. Type is "doctor_ready" or
// "doctor_msg".
type wsIncoming struct {
	Type         string `json:"type"`
	SessionToken string `json:"session_token"`
	DoctorID     uint16 `json:"doctor_id,omitempty"`
	Message      string `json:"message,omitempty"`
	Code         byte   `json:"code,omitempty"`
}
