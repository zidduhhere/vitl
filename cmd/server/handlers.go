package main

import (
	"encoding/base64"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/zidduhhere/vitl/internal/ehr"
	"github.com/zidduhhere/vitl/internal/media"
	"github.com/zidduhhere/vitl/internal/protocol"
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

	kickMu sync.Mutex
	kicks  map[uint64]chan struct{}

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

	patientKey := strconv.FormatUint(uint64(pkt.PatientID), 10)
	patient, err := s.store.GetPatient(patientKey)
	if err != nil {
		ack := protocol.SessionAckPacket{
			SessionToken: 0,
			StatusCode:   protocol.StatusPatientNotFound,
			ServerTime:   uint32(time.Now().Unix()),
		}
		s.udpConn.WriteToUDP(ack.Encode(), addr)
		return
	}

	sess := s.sessions.Create(pkt.WorkerID, pkt.PatientID, addr)

	status := protocol.StatusOK
	if !s.hub.HasClients() {
		status = protocol.StatusNoDoctorAvailable
	}
	ack := protocol.SessionAckPacket{
		SessionToken: sess.Token,
		StatusCode:   status,
		ServerTime:   uint32(time.Now().Unix()),
	}
	s.udpConn.WriteToUDP(ack.Encode(), addr)

	s.hub.Broadcast(wsEHRPush{
		Type:            "ehr_push",
		SessionToken:    strconv.FormatUint(uint64(sess.Token), 10),
		PatientID:       patient.ID,
		Demographics:    demographics{Name: patient.Name, Age: patient.Age, Sex: patient.Sex},
		KnownConditions: patient.Conditions,
		Allergies:       patient.Allergies,
		Medications:     patient.Medications,
		LastVisitNotes:  patient.LastVisitNotes,
		WorkerID:        strconv.FormatUint(uint64(pkt.WorkerID), 10),
	})
	s.hub.Broadcast(wsSessionStatus{Type: "session_status", SessionToken: strconv.FormatUint(uint64(sess.Token), 10), Status: "active"})
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
	s.udpConn.WriteToUDP(ack.Encode(), addr)

	if s.dedup.Seen(pkt.SessionToken, pkt.SeqNum) {
		return
	}

	s.hub.Broadcast(wsVitals{
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
	})
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
	s.udpConn.WriteToUDP(hb.Encode(), addr)
}

func (s *server) handleSessionEnd(data []byte) {
	pkt, err := protocol.DecodeSessionEnd(data)
	if err != nil {
		return
	}
	s.sessions.End(pkt.SessionToken)
	s.dedup.DropSession(pkt.SessionToken)
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
		s.udpConn.WriteToUDP(nack.Encode(), addr)
	}
	log.Printf("server: giving up on media transfer session=%d media=%d after %d NACK rounds", sessionToken, mediaID, maxNackRounds)
	s.reassembler.Drop(sessionToken, mediaID)
}

func (s *server) finishTransfer(sessionToken uint32, mediaID uint16, transfer *media.Transfer, addr *net.UDPAddr) {
	// Confirm completion to the client with an empty-missing NACK.
	nack := protocol.MediaNackPacket{SessionToken: sessionToken, MediaID: mediaID, MissingIndices: nil}
	s.udpConn.WriteToUDP(nack.Encode(), addr)

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
		s.udpConn.WriteToUDP(pkt.Encode(), sess.FieldAddr)
	case "doctor_msg":
		pkt := protocol.DoctorMsgPacket{SessionToken: sess.Token, Code: msg.Code}
		s.udpConn.WriteToUDP(pkt.Encode(), sess.FieldAddr)
	}
}
