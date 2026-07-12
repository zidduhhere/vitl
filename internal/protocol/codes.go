package protocol

// Packet type identifiers, shared across field-client and server so a single
// definition governs the wire format on both ends.
const (
	TypeSessionInit byte = 0x01
	TypeSessionAck  byte = 0x02
	TypeDoctorReady byte = 0x03
	TypeVitals      byte = 0x10
	TypeVitalsAck   byte = 0x11
	TypeDoctorMsg   byte = 0x12
	TypeHeartbeat   byte = 0x20
	TypeSessionEnd  byte = 0x30
	TypeAudioChunk  byte = 0x40
	TypeImageChunk  byte = 0x41
	TypeMediaNack   byte = 0x42
)

// SESSION_ACK status codes.
const (
	StatusOK                byte = 0x00
	StatusPatientNotFound   byte = 0x01
	StatusNoDoctorAvailable byte = 0x02
	// StatusPatientLocked is returned when a different worker already has
	// an active session open for the same patient.
	StatusPatientLocked byte = 0x03
	// StatusUnauthorized is returned when SESSION_INIT's AuthToken doesn't
	// match the value derived from the pre-shared key for that worker.
	StatusUnauthorized byte = 0x04
)

// DOCTOR_MSG instruction codes — coded, not free text, to stay lightweight
// over the constrained return path.
const (
	MsgStandBy         byte = 0x01
	MsgAdministerO2    byte = 0x02
	MsgEvacuate        byte = 0x03
	MsgContinueMonitor byte = 0x04
)
