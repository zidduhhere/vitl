// Package protocol defines the binary wire format shared by the field
// client and the server. Packets are fixed-width (except media payloads
// and the NACK index list) and encoded big-endian.
package protocol

import (
	"encoding/binary"
	"errors"
)

var (
	ErrInvalidLength    = errors.New("protocol: invalid packet length")
	ErrWrongType        = errors.New("protocol: unexpected packet type")
	ErrChecksumMismatch = errors.New("protocol: checksum mismatch")
)

// checksum is a simple additive checksum over the header+payload bytes.
// Good enough to catch corruption/truncation from a lossy link; not
// cryptographic.
func checksum(b []byte) uint16 {
	var sum uint16
	for _, v := range b {
		sum += uint16(v)
	}
	return sum
}

func appendChecksum(buf []byte) []byte {
	cs := checksum(buf)
	out := make([]byte, len(buf)+2)
	copy(out, buf)
	binary.BigEndian.PutUint16(out[len(buf):], cs)
	return out
}

func verifyChecksum(b []byte) ([]byte, error) {
	if len(b) < 2 {
		return nil, ErrInvalidLength
	}
	body := b[:len(b)-2]
	want := binary.BigEndian.Uint16(b[len(b)-2:])
	if checksum(body) != want {
		return nil, ErrChecksumMismatch
	}
	return body, nil
}

// PacketType peeks at the first byte of a raw datagram to identify it.
func PacketType(b []byte) (byte, error) {
	if len(b) < 1 {
		return 0, ErrInvalidLength
	}
	return b[0], nil
}

// The VITALS temp field is a single byte carrying temperature x10 for one
// decimal of precision (e.g. 36.8C -> 368), but 368 overflows a byte. We
// fix that by encoding relative to TempOffsetX10: byte = tempX10 - offset,
// which covers 30.0C-55.5C — comfortably spanning real hypothermia-to-fever
// range in a single byte.
const TempOffsetX10 = 300

// EncodeTempByte converts a temperature in x10 Celsius (e.g. 368 for
// 36.8C) into the wire byte.
func EncodeTempByte(tempX10 int) byte {
	v := tempX10 - TempOffsetX10
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	return byte(v)
}

// DecodeTempByte converts a wire byte back into x10 Celsius.
func DecodeTempByte(b byte) int {
	return int(b) + TempOffsetX10
}

// ---- SESSION_INIT (Field Worker -> Server) ----

type SessionInitPacket struct {
	WorkerID  uint32
	PatientID uint32
	Timestamp uint32
	// AuthToken is a pre-shared-key-derived credential proving this device
	// is a registered field worker for WorkerID (see internal/security).
	AuthToken uint32
}

func (p SessionInitPacket) Encode() []byte {
	buf := make([]byte, 17)
	buf[0] = TypeSessionInit
	binary.BigEndian.PutUint32(buf[1:5], p.WorkerID)
	binary.BigEndian.PutUint32(buf[5:9], p.PatientID)
	binary.BigEndian.PutUint32(buf[9:13], p.Timestamp)
	binary.BigEndian.PutUint32(buf[13:17], p.AuthToken)
	return appendChecksum(buf)
}

func DecodeSessionInit(b []byte) (SessionInitPacket, error) {
	if len(b) != 19 {
		return SessionInitPacket{}, ErrInvalidLength
	}
	body, err := verifyChecksum(b)
	if err != nil {
		return SessionInitPacket{}, err
	}
	if body[0] != TypeSessionInit {
		return SessionInitPacket{}, ErrWrongType
	}
	return SessionInitPacket{
		WorkerID:  binary.BigEndian.Uint32(body[1:5]),
		PatientID: binary.BigEndian.Uint32(body[5:9]),
		Timestamp: binary.BigEndian.Uint32(body[9:13]),
		AuthToken: binary.BigEndian.Uint32(body[13:17]),
	}, nil
}

// ---- SESSION_ACK (Server -> Field Worker) ----

type SessionAckPacket struct {
	SessionToken uint32
	StatusCode   byte
	ServerTime   uint32
}

func (p SessionAckPacket) Encode() []byte {
	buf := make([]byte, 10)
	buf[0] = TypeSessionAck
	binary.BigEndian.PutUint32(buf[1:5], p.SessionToken)
	buf[5] = p.StatusCode
	binary.BigEndian.PutUint32(buf[6:10], p.ServerTime)
	return appendChecksum(buf)
}

func DecodeSessionAck(b []byte) (SessionAckPacket, error) {
	if len(b) != 12 {
		return SessionAckPacket{}, ErrInvalidLength
	}
	body, err := verifyChecksum(b)
	if err != nil {
		return SessionAckPacket{}, err
	}
	if body[0] != TypeSessionAck {
		return SessionAckPacket{}, ErrWrongType
	}
	return SessionAckPacket{
		SessionToken: binary.BigEndian.Uint32(body[1:5]),
		StatusCode:   body[5],
		ServerTime:   binary.BigEndian.Uint32(body[6:10]),
	}, nil
}

// ---- DOCTOR_READY (Doctor -> Server -> Field Worker) ----

const doctorReadyMsgLen = 32

type DoctorReadyPacket struct {
	SessionToken uint32
	DoctorID     uint16
	Message      string // truncated/padded to 32 bytes on the wire
}

func (p DoctorReadyPacket) Encode() []byte {
	buf := make([]byte, 1+4+2+doctorReadyMsgLen)
	buf[0] = TypeDoctorReady
	binary.BigEndian.PutUint32(buf[1:5], p.SessionToken)
	binary.BigEndian.PutUint16(buf[5:7], p.DoctorID)
	msg := []byte(p.Message)
	if len(msg) > doctorReadyMsgLen {
		msg = msg[:doctorReadyMsgLen]
	}
	copy(buf[7:7+doctorReadyMsgLen], msg)
	return buf
}

func DecodeDoctorReady(b []byte) (DoctorReadyPacket, error) {
	if len(b) != 1+4+2+doctorReadyMsgLen {
		return DoctorReadyPacket{}, ErrInvalidLength
	}
	if b[0] != TypeDoctorReady {
		return DoctorReadyPacket{}, ErrWrongType
	}
	msgBytes := b[7 : 7+doctorReadyMsgLen]
	end := len(msgBytes)
	for end > 0 && msgBytes[end-1] == 0 {
		end--
	}
	return DoctorReadyPacket{
		SessionToken: binary.BigEndian.Uint32(b[1:5]),
		DoctorID:     binary.BigEndian.Uint16(b[5:7]),
		Message:      string(msgBytes[:end]),
	}, nil
}

// ---- VITALS (Field Worker -> Server) ----

type VitalsPacket struct {
	SessionToken uint32
	SeqNum       uint16
	HeartRate    byte
	SpO2         byte
	BPSystolic   byte
	BPDiastolic  byte
	Temp         byte // scaled int, x10 (e.g. 372 == 37.2C)
	DeltaFlag    byte
	Timestamp    uint32
}

func (p VitalsPacket) Encode() []byte {
	buf := make([]byte, 17)
	buf[0] = TypeVitals
	binary.BigEndian.PutUint32(buf[1:5], p.SessionToken)
	binary.BigEndian.PutUint16(buf[5:7], p.SeqNum)
	buf[7] = p.HeartRate
	buf[8] = p.SpO2
	buf[9] = p.BPSystolic
	buf[10] = p.BPDiastolic
	buf[11] = p.Temp
	buf[12] = p.DeltaFlag
	binary.BigEndian.PutUint32(buf[13:17], p.Timestamp)
	return appendChecksum(buf)
}

func DecodeVitals(b []byte) (VitalsPacket, error) {
	if len(b) != 19 {
		return VitalsPacket{}, ErrInvalidLength
	}
	body, err := verifyChecksum(b)
	if err != nil {
		return VitalsPacket{}, err
	}
	if body[0] != TypeVitals {
		return VitalsPacket{}, ErrWrongType
	}
	return VitalsPacket{
		SessionToken: binary.BigEndian.Uint32(body[1:5]),
		SeqNum:       binary.BigEndian.Uint16(body[5:7]),
		HeartRate:    body[7],
		SpO2:         body[8],
		BPSystolic:   body[9],
		BPDiastolic:  body[10],
		Temp:         body[11],
		DeltaFlag:    body[12],
		Timestamp:    binary.BigEndian.Uint32(body[13:17]),
	}, nil
}

// ---- VITALS_ACK (Server -> Field Worker) ----

type VitalsAckPacket struct {
	SessionToken uint32
	AckSeqNum    uint16
}

func (p VitalsAckPacket) Encode() []byte {
	buf := make([]byte, 7)
	buf[0] = TypeVitalsAck
	binary.BigEndian.PutUint32(buf[1:5], p.SessionToken)
	binary.BigEndian.PutUint16(buf[5:7], p.AckSeqNum)
	return buf
}

func DecodeVitalsAck(b []byte) (VitalsAckPacket, error) {
	if len(b) != 7 {
		return VitalsAckPacket{}, ErrInvalidLength
	}
	if b[0] != TypeVitalsAck {
		return VitalsAckPacket{}, ErrWrongType
	}
	return VitalsAckPacket{
		SessionToken: binary.BigEndian.Uint32(b[1:5]),
		AckSeqNum:    binary.BigEndian.Uint16(b[5:7]),
	}, nil
}

// ---- DOCTOR_MSG (Doctor -> Server -> Field Worker) ----

type DoctorMsgPacket struct {
	SessionToken uint32
	Code         byte
}

func (p DoctorMsgPacket) Encode() []byte {
	buf := make([]byte, 6)
	buf[0] = TypeDoctorMsg
	binary.BigEndian.PutUint32(buf[1:5], p.SessionToken)
	buf[5] = p.Code
	return buf
}

func DecodeDoctorMsg(b []byte) (DoctorMsgPacket, error) {
	if len(b) != 6 {
		return DoctorMsgPacket{}, ErrInvalidLength
	}
	if b[0] != TypeDoctorMsg {
		return DoctorMsgPacket{}, ErrWrongType
	}
	return DoctorMsgPacket{
		SessionToken: binary.BigEndian.Uint32(b[1:5]),
		Code:         b[5],
	}, nil
}

// ---- HEARTBEAT (either direction) ----

type HeartbeatPacket struct {
	SessionToken uint32
}

func (p HeartbeatPacket) Encode() []byte {
	buf := make([]byte, 5)
	buf[0] = TypeHeartbeat
	binary.BigEndian.PutUint32(buf[1:5], p.SessionToken)
	return buf
}

func DecodeHeartbeat(b []byte) (HeartbeatPacket, error) {
	if len(b) != 5 {
		return HeartbeatPacket{}, ErrInvalidLength
	}
	if b[0] != TypeHeartbeat {
		return HeartbeatPacket{}, ErrWrongType
	}
	return HeartbeatPacket{SessionToken: binary.BigEndian.Uint32(b[1:5])}, nil
}

// ---- SESSION_END (Field Worker -> Server) ----

type SessionEndPacket struct {
	SessionToken uint32
}

func (p SessionEndPacket) Encode() []byte {
	buf := make([]byte, 5)
	buf[0] = TypeSessionEnd
	binary.BigEndian.PutUint32(buf[1:5], p.SessionToken)
	return buf
}

func DecodeSessionEnd(b []byte) (SessionEndPacket, error) {
	if len(b) != 5 {
		return SessionEndPacket{}, ErrInvalidLength
	}
	if b[0] != TypeSessionEnd {
		return SessionEndPacket{}, ErrWrongType
	}
	return SessionEndPacket{SessionToken: binary.BigEndian.Uint32(b[1:5])}, nil
}

// ---- AUDIO_CHUNK / IMAGE_CHUNK (Field Worker -> Server) ----

// MaxChunkPayload keeps datagrams well under typical MTU to avoid IP-layer
// fragmentation, which makes loss worse.
const MaxChunkPayload = 480

type MediaChunkPacket struct {
	Type         byte // TypeAudioChunk or TypeImageChunk
	SessionToken uint32
	MediaID      uint16
	ChunkIndex   uint16
	TotalChunks  uint16
	Payload      []byte
}

func (p MediaChunkPacket) Encode() []byte {
	buf := make([]byte, 11+len(p.Payload))
	buf[0] = p.Type
	binary.BigEndian.PutUint32(buf[1:5], p.SessionToken)
	binary.BigEndian.PutUint16(buf[5:7], p.MediaID)
	binary.BigEndian.PutUint16(buf[7:9], p.ChunkIndex)
	binary.BigEndian.PutUint16(buf[9:11], p.TotalChunks)
	copy(buf[11:], p.Payload)
	return appendChecksum(buf)
}

func DecodeMediaChunk(b []byte) (MediaChunkPacket, error) {
	if len(b) < 13 {
		return MediaChunkPacket{}, ErrInvalidLength
	}
	body, err := verifyChecksum(b)
	if err != nil {
		return MediaChunkPacket{}, err
	}
	if body[0] != TypeAudioChunk && body[0] != TypeImageChunk {
		return MediaChunkPacket{}, ErrWrongType
	}
	payload := make([]byte, len(body)-11)
	copy(payload, body[11:])
	return MediaChunkPacket{
		Type:         body[0],
		SessionToken: binary.BigEndian.Uint32(body[1:5]),
		MediaID:      binary.BigEndian.Uint16(body[5:7]),
		ChunkIndex:   binary.BigEndian.Uint16(body[7:9]),
		TotalChunks:  binary.BigEndian.Uint16(body[9:11]),
		Payload:      payload,
	}, nil
}

// ---- MEDIA_NACK (Server -> Field Worker) ----

type MediaNackPacket struct {
	SessionToken   uint32
	MediaID        uint16
	MissingIndices []uint16
}

func (p MediaNackPacket) Encode() []byte {
	buf := make([]byte, 9+2*len(p.MissingIndices))
	buf[0] = TypeMediaNack
	binary.BigEndian.PutUint32(buf[1:5], p.SessionToken)
	binary.BigEndian.PutUint16(buf[5:7], p.MediaID)
	binary.BigEndian.PutUint16(buf[7:9], uint16(len(p.MissingIndices)))
	for i, idx := range p.MissingIndices {
		binary.BigEndian.PutUint16(buf[9+2*i:11+2*i], idx)
	}
	return appendChecksum(buf)
}

func DecodeMediaNack(b []byte) (MediaNackPacket, error) {
	if len(b) < 11 {
		return MediaNackPacket{}, ErrInvalidLength
	}
	body, err := verifyChecksum(b)
	if err != nil {
		return MediaNackPacket{}, err
	}
	if body[0] != TypeMediaNack {
		return MediaNackPacket{}, ErrWrongType
	}
	count := binary.BigEndian.Uint16(body[7:9])
	if len(body) != 9+2*int(count) {
		return MediaNackPacket{}, ErrInvalidLength
	}
	missing := make([]uint16, count)
	for i := range missing {
		missing[i] = binary.BigEndian.Uint16(body[9+2*i : 11+2*i])
	}
	return MediaNackPacket{
		SessionToken:   binary.BigEndian.Uint32(body[1:5]),
		MediaID:        binary.BigEndian.Uint16(body[5:7]),
		MissingIndices: missing,
	}, nil
}
