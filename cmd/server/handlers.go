package main

import (
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/zidduhhere/vitl/internal/ehr"
	"github.com/zidduhhere/vitl/internal/media"
	"github.com/zidduhhere/vitl/internal/protocol"
	"github.com/zidduhhere/vitl/internal/security"
	"github.com/zidduhhere/vitl/internal/session"
	"github.com/zidduhhere/vitl/internal/transport"
)

// server bundles the shared dependencies UDP packet handling needs.
type server struct {
	udpConn     *net.UDPConn
	store       *ehr.Store
	sessions    *session.Manager
	hub         *Hub
	reassembler *media.Reassembler
	dedup       *transport.SeqDedup
	securityKey [security.KeySize]byte

	kickMu sync.Mutex
	kicks  map[uint64]chan struct{}

	// stateMu guards the last-known-state cache below, which lets a
	// dashboard that reconnects mid-session (e.g. laptop slept, wifi
	// blipped) replay where things stand instead of seeing nothing until
	// the next live event.
	stateMu       sync.Mutex
	lastEHR       map[uint32]wsEHRPush
	sessionStatus map[uint32]wsSessionStatus
	lastVitals    map[uint32]wsVitals

	// Rate-limits the "unexpected packet type" log below. This fires on
	// any stray traffic that lands on the UDP port (e.g. unrelated
	// multicast/discovery packets from other devices/services on the
	// same network that happen to target the same port number) and can
	// otherwise flood the log at multiple lines per second.
	unknownPktLogMu  sync.Mutex
	unknownPktLogged time.Time
}

func mediaKey(sessionToken uint32, mediaID uint16) uint64 {
	return uint64(sessionToken)<<16 | uint64(mediaID)
}

// writeUDP seals a plaintext packet with the shared PSK-derived key before
// putting it on the field link — every outbound datagram goes through here
// so no send site can accidentally skip encryption.
func (s *server) writeUDP(plaintext []byte, addr *net.UDPAddr) {
	sealed, err := security.Seal(s.securityKey, plaintext)
	if err != nil {
		log.Printf("server: failed to seal outbound packet: %v", err)
		return
	}
	s.udpConn.WriteToUDP(sealed, addr)
}

func (s *server) handlePacket(addr *net.UDPAddr, data []byte) {
	t, err := protocol.PacketType(data)
	if err != nil {
		return
	}

	switch t {
	case protocol.TypeSessionInit:
		s.handleSessionInit(addr, data)
	case protocol.TypeVitals:
		s.handleVitals(addr, data)
	case protocol.TypeHeartbeat:
		s.handleHeartbeat(addr, data)
	case protocol.TypeSessionEnd:
		s.handleSessionEnd(data)
	case protocol.TypeAudioChunk, protocol.TypeImageChunk:
		s.handleMediaChunk(addr, data)
	default:
		s.logUnknownPacket(t, addr)
	}
}

// logUnknownPacket logs at most once every 5 seconds so stray traffic
// (e.g. unrelated multicast packets from other devices on the network)
// can't flood the log.
func (s *server) logUnknownPacket(t byte, addr *net.UDPAddr) {
	s.unknownPktLogMu.Lock()
	defer s.unknownPktLogMu.Unlock()

	if time.Since(s.unknownPktLogged) < 5*time.Second {
		return
	}
	s.unknownPktLogged = time.Now()
	log.Printf("server: ignoring unexpected packet type 0x%02x from %s (further occurrences suppressed for 5s)", t, addr)
}

func (s *server) handleSessionInit(addr *net.UDPAddr, data []byte) {
	pkt, err := protocol.DecodeSessionInit(data)
	if err != nil {
		log.Printf("server: bad SESSION_INIT from %s: %v", addr, err)
		return
	}

	if pkt.AuthToken != security.DeriveWorkerToken(s.securityKey, pkt.WorkerID) {
		log.Printf("server: rejecting SESSION_INIT from %s: bad auth token for worker=%d", addr, pkt.WorkerID)
		ack := protocol.SessionAckPacket{
			SessionToken: 0,
			StatusCode:   protocol.StatusUnauthorized,
			ServerTime:   uint32(time.Now().Unix()),
		}
		s.writeUDP(ack.Encode(), addr)
		return
	}

	patientKey := strconv.FormatUint(uint64(pkt.PatientID), 10)
	patient, err := s.store.GetPatient(patientKey)
	if err != nil {
		ack := protocol.SessionAckPacket{
			SessionToken: 0,
			StatusCode:   protocol.StatusPatientNotFound,
			ServerTime:   uint32(time.Now().Unix()),
		}
		s.writeUDP(ack.Encode(), addr)
		return
	}

	sess, reused, err := s.sessions.GetOrCreate(pkt.WorkerID, pkt.PatientID, addr)
	if err == session.ErrPatientLocked {
		ack := protocol.SessionAckPacket{
			SessionToken: 0,
			StatusCode:   protocol.StatusPatientLocked,
			ServerTime:   uint32(time.Now().Unix()),
		}
		s.writeUDP(ack.Encode(), addr)
		return
	}

	noDoctor := !s.hub.HasClients()
	status := protocol.StatusOK
	if noDoctor {
		status = protocol.StatusNoDoctorAvailable
	}
	ack := protocol.SessionAckPacket{
		SessionToken: sess.Token,
		StatusCode:   status,
		ServerTime:   uint32(time.Now().Unix()),
	}
	s.writeUDP(ack.Encode(), addr)

	// A reused session means this SESSION_INIT is a retry for an ACK that
	// never made it back (or was lost) — the dashboard already got the EHR
	// push and "active" status the first time, so resending the ACK above
	// is all that's needed here.
	if reused {
		return
	}

	ehrPush := wsEHRPush{
		Type:            "ehr_push",
		SessionToken:    strconv.FormatUint(uint64(sess.Token), 10),
		PatientID:       patient.ID,
		Demographics:    demographics{Name: patient.Name, Age: patient.Age, Sex: patient.Sex},
		KnownConditions: patient.Conditions,
		Allergies:       patient.Allergies,
		Medications:     patient.Medications,
		LastVisitNotes:  patient.LastVisitNotes,
		WorkerID:        strconv.FormatUint(uint64(pkt.WorkerID), 10),
	}
	// Queued marks a session that started with no doctor connected — the
	// dashboard's WS Snapshot hook (see snapshot() below) notifies the next
	// doctor who connects rather than this session silently sitting unseen.
	sessionStatus := wsSessionStatus{Type: "session_status", SessionToken: strconv.FormatUint(uint64(sess.Token), 10), Status: "active", Queued: noDoctor}

	s.recordSessionState(sess.Token, ehrPush, sessionStatus)
	s.hub.Broadcast(ehrPush)
	s.hub.Broadcast(sessionStatus)
}

func (s *server) handleVitals(addr *net.UDPAddr, data []byte) {
	pkt, err := protocol.DecodeVitals(data)
	if err != nil {
		return
	}
	if _, ok := s.sessions.Get(pkt.SessionToken); !ok {
		return // unknown/expired session, drop silently — freshness over completeness
	}

	// ACK immediately regardless of dedup outcome so the field client's
	// short retry window doesn't fire needlessly.
	ack := protocol.VitalsAckPacket{SessionToken: pkt.SessionToken, AckSeqNum: pkt.SeqNum}
	s.writeUDP(ack.Encode(), addr)

	if s.dedup.Seen(pkt.SessionToken, pkt.SeqNum) {
		return
	}

	vitals := wsVitals{
		Type:         "vitals",
		SessionToken: strconv.FormatUint(uint64(pkt.SessionToken), 10),
		SeqNum:       pkt.SeqNum,
		HeartRate:    pkt.HeartRate,
		SpO2:         pkt.SpO2,
		BPSystolic:   pkt.BPSystolic,
		BPDiastolic:  pkt.BPDiastolic,
		TempC:        float64(protocol.DecodeTempByte(pkt.Temp)) / 10.0,
		DeltaFlag:    pkt.DeltaFlag,
		Timestamp:    pkt.Timestamp,
	}
	s.recordLastVitals(pkt.SessionToken, vitals)
	s.hub.Broadcast(vitals)
}

func (s *server) handleHeartbeat(addr *net.UDPAddr, data []byte) {
	pkt, err := protocol.DecodeHeartbeat(data)
	if err != nil {
		return
	}
	if _, ok := s.sessions.Get(pkt.SessionToken); !ok {
		return
	}
	hb := protocol.HeartbeatPacket{SessionToken: pkt.SessionToken}
	s.writeUDP(hb.Encode(), addr)
}

func (s *server) handleSessionEnd(data []byte) {
	pkt, err := protocol.DecodeSessionEnd(data)
	if err != nil {
		return
	}
	s.sessions.End(pkt.SessionToken)
	s.dedup.DropSession(pkt.SessionToken)
	s.clearSessionState(pkt.SessionToken)
	s.hub.Broadcast(wsSessionStatus{Type: "session_status", SessionToken: strconv.FormatUint(uint64(pkt.SessionToken), 10), Status: "ended"})
}

// nackInterval/maxNackRounds govern how persistently the server chases
// missing media chunks before giving up on a transfer.
const (
	nackInterval  = 400 * time.Millisecond
	maxNackRounds = 15
)

func (s *server) handleMediaChunk(addr *net.UDPAddr, data []byte) {
	pkt, err := protocol.DecodeMediaChunk(data)
	if err != nil {
		return
	}
	if _, ok := s.sessions.Get(pkt.SessionToken); !ok {
		return
	}

	transfer := s.reassembler.GetOrCreate(pkt.SessionToken, pkt.MediaID, pkt.Type, pkt.TotalChunks)
	complete := transfer.AddChunk(pkt.ChunkIndex, pkt.Payload)

	// Exactly one watcher goroutine owns completion-checking and NACK
	// sending for a given transfer, so there is a single place that can
	// ever broadcast the finished media to the dashboard — no risk of two
	// goroutines both observing "complete" and double-sending.
	k := mediaKey(pkt.SessionToken, pkt.MediaID)
	s.kickMu.Lock()
	kick, exists := s.kicks[k]
	if !exists {
		kick = make(chan struct{}, 1)
		s.kicks[k] = kick
	}
	s.kickMu.Unlock()
	if !exists {
		go s.watchTransfer(pkt.SessionToken, pkt.MediaID, transfer, addr, kick)
	}

	// Only wake the watcher early once this chunk completed the transfer —
	// waking it on every partial chunk would fire NACKs mid-burst, before
	// the rest of the round has even arrived. Routine gap-filling for an
	// incomplete transfer is handled by the watcher's own ticker instead.
	if complete {
		select {
		case kick <- struct{}{}:
		default:
		}
	}
}

// watchTransfer is the sole owner of a transfer's lifecycle from first
// chunk to completion: it wakes on every new chunk (via kick) or on a
// periodic tick, checks for completeness, sends a NACK listing whatever is
// still missing, and finishes the transfer (once) when nothing is.
func (s *server) watchTransfer(sessionToken uint32, mediaID uint16, transfer *media.Transfer, addr *net.UDPAddr, kick chan struct{}) {
	defer func() {
		s.kickMu.Lock()
		delete(s.kicks, mediaKey(sessionToken, mediaID))
		s.kickMu.Unlock()
	}()

	ticker := time.NewTicker(nackInterval)
	defer ticker.Stop()

	for round := 0; round < maxNackRounds; round++ {
		select {
		case <-kick:
		case <-ticker.C:
		}

		missing := transfer.MissingIndices()
		if len(missing) == 0 {
			s.finishTransfer(sessionToken, mediaID, transfer, addr)
			return
		}
		nack := protocol.MediaNackPacket{SessionToken: sessionToken, MediaID: mediaID, MissingIndices: missing}
		s.writeUDP(nack.Encode(), addr)
	}
	log.Printf("server: giving up on media transfer session=%d media=%d after %d NACK rounds", sessionToken, mediaID, maxNackRounds)
	kind := "audio"
	if transfer.ChunkType == protocol.TypeImageChunk {
		kind = "image"
	}
	s.hub.Broadcast(wsMediaStatus{
		Type:         "media_failed",
		SessionToken: strconv.FormatUint(uint64(sessionToken), 10),
		MediaID:      mediaID,
		Kind:         kind,
		Reason:       fmt.Sprintf("gave up after %d NACK rounds with chunks still missing", maxNackRounds),
	})
	s.reassembler.Drop(sessionToken, mediaID)
}

func (s *server) finishTransfer(sessionToken uint32, mediaID uint16, transfer *media.Transfer, addr *net.UDPAddr) {
	// Confirm completion to the client with an empty-missing NACK.
	nack := protocol.MediaNackPacket{SessionToken: sessionToken, MediaID: mediaID, MissingIndices: nil}
	s.writeUDP(nack.Encode(), addr)

	kind := "audio"
	if transfer.ChunkType == protocol.TypeImageChunk {
		kind = "image"
	}
	s.hub.Broadcast(wsMedia{
		Type:         "media",
		SessionToken: strconv.FormatUint(uint64(sessionToken), 10),
		MediaID:      mediaID,
		Kind:         kind,
		DataBase64:   base64.StdEncoding.EncodeToString(transfer.Assemble()),
	})
	s.reassembler.Drop(sessionToken, mediaID)
}

// handleDashboardMessage relays a doctor's DOCTOR_READY/DOCTOR_MSG back to
// the field client over UDP.
func (s *server) handleDashboardMessage(msg wsIncoming) {
	token, err := strconv.ParseUint(msg.SessionToken, 10, 32)
	if err != nil {
		return
	}
	sess, ok := s.sessions.Get(uint32(token))
	if !ok {
		return
	}

	switch msg.Type {
	case "doctor_ready":
		s.sessions.SetDoctorReady(sess.Token)
		pkt := protocol.DoctorReadyPacket{SessionToken: sess.Token, DoctorID: msg.DoctorID, Message: msg.Message}
		s.writeUDP(pkt.Encode(), sess.FieldAddr)
	case "doctor_msg":
		pkt := protocol.DoctorMsgPacket{SessionToken: sess.Token, Code: msg.Code}
		s.writeUDP(pkt.Encode(), sess.FieldAddr)
	}
}

func (s *server) recordSessionState(token uint32, ehrPush wsEHRPush, status wsSessionStatus) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.lastEHR[token] = ehrPush
	s.sessionStatus[token] = status
}

func (s *server) recordLastVitals(token uint32, vitals wsVitals) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if _, ok := s.sessionStatus[token]; !ok {
		return // session already ended/unknown, don't resurrect it in the cache
	}
	s.lastVitals[token] = vitals
}

func (s *server) clearSessionState(token uint32) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	delete(s.lastEHR, token)
	delete(s.sessionStatus, token)
	delete(s.lastVitals, token)
}

// snapshot builds the set of messages needed to bring a newly (re)connected
// dashboard up to date on every still-active session, without waiting for
// the next live event. It doubles as the doctor-queueing notification: any
// session that started with no doctor connected (status.Queued) is
// announced here — to whichever doctor connects next — and then cleared so
// it isn't re-announced as newly-queued on a later reconnect.
func (s *server) snapshot() []interface{} {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	msgs := make([]interface{}, 0, len(s.sessionStatus)*3)
	for token, status := range s.sessionStatus {
		if ehrPush, ok := s.lastEHR[token]; ok {
			msgs = append(msgs, ehrPush)
		}
		msgs = append(msgs, status)
		if status.Queued {
			status.Queued = false
			s.sessionStatus[token] = status
		}
		if vitals, ok := s.lastVitals[token]; ok {
			msgs = append(msgs, vitals)
		}
	}
	return msgs
}
