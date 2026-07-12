// Command field-client simulates a low-power field worker device: it
// establishes a session with the server, streams simulated vitals over the
// constrained UDP link, and (optionally) sends a chunked audio or image
// payload using the sliding-window media transport.
package main

import (
	"flag"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/zidduhhere/vitl/internal/media"
	"github.com/zidduhhere/vitl/internal/protocol"
	"github.com/zidduhhere/vitl/internal/security"
	"github.com/zidduhhere/vitl/internal/transport"
)

// mediaNackRegistry routes incoming MEDIA_NACK packets to the specific
// transfer they belong to, keyed by media_id. This lets multiple media
// transfers (e.g. audio and image) run concurrently over the same UDP
// socket without stealing each other's NACKs.
type mediaNackRegistry struct {
	mu   sync.Mutex
	subs map[uint16]chan protocol.MediaNackPacket
}

func newMediaNackRegistry() *mediaNackRegistry {
	return &mediaNackRegistry{subs: make(map[uint16]chan protocol.MediaNackPacket)}
}

func (r *mediaNackRegistry) subscribe(mediaID uint16) chan protocol.MediaNackPacket {
	ch := make(chan protocol.MediaNackPacket, 16)
	r.mu.Lock()
	r.subs[mediaID] = ch
	r.mu.Unlock()
	return ch
}

func (r *mediaNackRegistry) unsubscribe(mediaID uint16) {
	r.mu.Lock()
	delete(r.subs, mediaID)
	r.mu.Unlock()
}

func (r *mediaNackRegistry) dispatch(pkt protocol.MediaNackPacket) {
	r.mu.Lock()
	ch, ok := r.subs[pkt.MediaID]
	r.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- pkt:
	default:
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	serverAddr := flag.String("server", "127.0.0.1:9000", "server UDP address")
	workerID := flag.Uint("worker-id", 1, "field worker/device id")
	patientID := flag.Uint("patient-id", 1001, "patient id to open a session for")
	vitalsInterval := flag.Duration("vitals-interval", 2*time.Second, "delay between VITALS packets")
	audioFile := flag.String("audio-file", "", "optional WAV file to encode+send as a chunked AUDIO_CHUNK transfer")
	imageFile := flag.String("image-file", "", "optional JPEG/PNG file to encode+send as a chunked IMAGE_CHUNK transfer")
	psk := flag.String("psk", envOr("VITALLINK_PSK", "vitallink-demo-psk-change-me"), "pre-shared key securing the field<->server UDP link (also settable via VITALLINK_PSK); must match the server")
	flag.Parse()

	securityKey := security.DeriveKey(*psk)

	raddr, err := net.ResolveUDPAddr("udp", *serverAddr)
	if err != nil {
		log.Fatalf("field-client: bad server address: %v", err)
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		log.Fatalf("field-client: dial failed: %v", err)
	}
	defer conn.Close()

	// A single goroutine owns the socket read side and fans incoming
	// packets out by type, so senders never race each other on Read.
	sessionAckCh := make(chan []byte, 4)
	vitalsAckCh := make(chan []byte, 16)
	doctorReadyCh := make(chan protocol.DoctorReadyPacket, 4)
	doctorMsgCh := make(chan protocol.DoctorMsgPacket, 16)
	nackRegistry := newMediaNackRegistry()
	go receiveLoop(conn, securityKey, sessionAckCh, vitalsAckCh, doctorReadyCh, doctorMsgCh, nackRegistry)

	// Vitals packets get priority over media chunks on the outbound socket
	// (edge-cases.md #12: a delayed heart-rate reading matters more than a
	// delayed image chunk). SealingWriter encrypts every payload the
	// sender writes, so the priority scheduling and the encryption are
	// both applied at the same single choke point.
	sealed := security.SealingWriter{W: conn, Key: securityKey}
	sender := transport.NewPrioritySender(sealed)
	defer sender.Close()

	go func() {
		for {
			select {
			case dr := <-doctorReadyCh:
				log.Printf("field-client: doctor ready (doctor_id=%d msg=%q)", dr.DoctorID, dr.Message)
			case dm := <-doctorMsgCh:
				log.Printf("field-client: doctor instruction code=0x%02x", dm.Code)
			}
		}
	}()

	// ---- Session handshake ----
	initPkt := protocol.SessionInitPacket{
		WorkerID:  uint32(*workerID),
		PatientID: uint32(*patientID),
		Timestamp: uint32(time.Now().Unix()),
		AuthToken: security.DeriveWorkerToken(securityKey, uint32(*workerID)),
	}
	resp, err := transport.SendWithRetry(
		sender.VitalsWrite,
		sessionAckCh,
		initPkt.Encode(),
		2*time.Second, 5,
		func(b []byte) bool {
			t, err := protocol.PacketType(b)
			return err == nil && t == protocol.TypeSessionAck
		},
	)
	if err != nil {
		log.Fatalf("field-client: SESSION_INIT failed after retries: %v", err)
	}
	ack, err := protocol.DecodeSessionAck(resp)
	if err != nil {
		log.Fatalf("field-client: malformed SESSION_ACK: %v", err)
	}
	switch ack.StatusCode {
	case protocol.StatusPatientNotFound:
		log.Fatalf("field-client: server reports patient %d not found", *patientID)
	case protocol.StatusUnauthorized:
		log.Fatalf("field-client: server rejected our auth token — check -psk matches the server's")
	case protocol.StatusPatientLocked:
		log.Fatalf("field-client: patient %d already has an active session with another worker", *patientID)
	case protocol.StatusNoDoctorAvailable:
		log.Printf("field-client: session %d established, but no doctor is connected yet (queued)", ack.SessionToken)
	default:
		log.Printf("field-client: session %d established", ack.SessionToken)
	}
	sessionToken := ack.SessionToken

	// ---- Graceful shutdown on Ctrl-C ----
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	stopVitals := make(chan struct{})
	go func() {
		<-sigCh
		log.Printf("field-client: shutting down, sending SESSION_END")
		end := protocol.SessionEndPacket{SessionToken: sessionToken}
		sender.VitalsWrite(end.Encode())
		close(stopVitals)
		time.Sleep(200 * time.Millisecond)
		os.Exit(0)
	}()

	// ---- Optional media transfer, sent once after the handshake ----
	if *audioFile != "" {
		go sendAudio(sender, nackRegistry, sessionToken, *audioFile)
	}
	if *imageFile != "" {
		go sendImage(sender, nackRegistry, sessionToken, *imageFile)
	}

	// ---- Steady-state vitals loop ----
	runVitalsLoop(sender, vitalsAckCh, sessionToken, *vitalsInterval, stopVitals)
}

func receiveLoop(conn *net.UDPConn, key [security.KeySize]byte, sessionAckCh, vitalsAckCh chan []byte, doctorReadyCh chan protocol.DoctorReadyPacket, doctorMsgCh chan protocol.DoctorMsgPacket, nackRegistry *mediaNackRegistry) {
	buf := make([]byte, 2048)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			continue
		}
		data, err := security.Open(key, buf[:n])
		if err != nil {
			// Wrong/missing key or corrupted datagram — same as a
			// checksum failure: drop silently, no processing.
			continue
		}

		t, err := protocol.PacketType(data)
		if err != nil {
			continue
		}
		switch t {
		case protocol.TypeSessionAck:
			select {
			case sessionAckCh <- data:
			default:
			}
		case protocol.TypeVitalsAck:
			select {
			case vitalsAckCh <- data:
			default:
			}
		case protocol.TypeDoctorReady:
			if p, err := protocol.DecodeDoctorReady(data); err == nil {
				select {
				case doctorReadyCh <- p:
				default:
				}
			}
		case protocol.TypeDoctorMsg:
			if p, err := protocol.DecodeDoctorMsg(data); err == nil {
				select {
				case doctorMsgCh <- p:
				default:
				}
			}
		case protocol.TypeMediaNack:
			if p, err := protocol.DecodeMediaNack(data); err == nil {
				nackRegistry.dispatch(p)
			}
		}
	}
}

// maxOfflineBuffer bounds the offline backlog (edge-cases.md: field device
// goes fully offline for a period). At a ~2s vitals cadence this covers a
// couple of minutes of backlog — enough to ride out a real dead zone
// without unbounded memory growth. Freshness over completeness still
// governs: once full, the oldest backlog entry is dropped, not the newest
// reading.
const maxOfflineBuffer = 60

// runVitalsLoop simulates a patient's vitals with a small random walk and
// streams them. Freshness over completeness: short timeout, few retries —
// an ACK that never arrives just means we move on to the next reading. If
// the link goes fully offline (not just lossy), unACKed readings are
// buffered and replayed as a backlog burst once the link comes back,
// rather than being permanently lost.
func runVitalsLoop(sender *transport.PrioritySender, vitalsAckCh chan []byte, sessionToken uint32, interval time.Duration, stop <-chan struct{}) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	hr, spo2, sys, dia, tempX10 := 78, 97, 118, 76, 368 // tempX10 is Celsius x10 (36.8C)

	var seq uint16
	var offlineBuffer [][]byte
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			hr = clamp(hr+rng.Intn(5)-2, 50, 160)
			spo2 = clamp(spo2+rng.Intn(3)-1, 85, 100)
			sys = clamp(sys+rng.Intn(5)-2, 90, 180)
			dia = clamp(dia+rng.Intn(3)-1, 50, 110)
			tempX10 = clamp(tempX10+rng.Intn(3)-1, 350, 410)

			pkt := protocol.VitalsPacket{
				SessionToken: sessionToken,
				SeqNum:       seq,
				HeartRate:    byte(hr),
				SpO2:         byte(spo2),
				BPSystolic:   byte(sys),
				BPDiastolic:  byte(dia),
				Temp:         protocol.EncodeTempByte(tempX10),
				DeltaFlag:    0,
				Timestamp:    uint32(time.Now().Unix()),
			}
			seq++
			encoded := pkt.Encode()

			_, err := transport.SendWithRetry(
				sender.VitalsWrite,
				vitalsAckCh,
				encoded,
				400*time.Millisecond, 1,
				func(b []byte) bool {
					va, err := protocol.DecodeVitalsAck(b)
					return err == nil && va.SessionToken == sessionToken && va.AckSeqNum == pkt.SeqNum
				},
			)
			if err != nil {
				offlineBuffer = append(offlineBuffer, encoded)
				if len(offlineBuffer) > maxOfflineBuffer {
					offlineBuffer = offlineBuffer[1:]
				}
				log.Printf("field-client: vitals seq=%d unacked, buffered (offline backlog=%d)", pkt.SeqNum, len(offlineBuffer))
				continue
			}

			if len(offlineBuffer) > 0 {
				log.Printf("field-client: link back — flushing %d buffered vitals reading(s)", len(offlineBuffer))
				for _, backlogPkt := range offlineBuffer {
					sender.VitalsWrite(backlogPkt) // best-effort, no retry — these are already-stale backlog
				}
				offlineBuffer = nil
			}
			log.Printf("field-client: vitals seq=%d acked (hr=%d spo2=%d bp=%d/%d temp=%.1f)", pkt.SeqNum, hr, spo2, sys, dia, float64(tempX10)/10)
		}
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func sendAudio(sender *transport.PrioritySender, nackRegistry *mediaNackRegistry, sessionToken uint32, path string) {
	encoded, err := media.EncodeAudioOpus(path, 16)
	if err != nil {
		log.Printf("field-client: audio encode failed: %v", err)
		return
	}
	sendMedia(sender, nackRegistry, sessionToken, protocol.TypeAudioChunk, encoded, "audio")
}

func sendImage(sender *transport.PrioritySender, nackRegistry *mediaNackRegistry, sessionToken uint32, path string) {
	encoded, err := media.EncodeImageJPEG(path, 320, 40)
	if err != nil {
		log.Printf("field-client: image encode failed: %v", err)
		return
	}
	sendMedia(sender, nackRegistry, sessionToken, protocol.TypeImageChunk, encoded, "image")
}

func sendMedia(sender *transport.PrioritySender, nackRegistry *mediaNackRegistry, sessionToken uint32, chunkType byte, payload []byte, label string) {
	mediaID := uint16(rand.New(rand.NewSource(time.Now().UnixNano())).Intn(65536))
	nackCh := nackRegistry.subscribe(mediaID)
	defer nackRegistry.unsubscribe(mediaID)

	mediaSender := &transport.MediaSender{
		Write:        sender.MediaWrite,
		NackCh:       nackCh,
		SessionToken: sessionToken,
		MediaID:      mediaID,
		ChunkType:    chunkType,
		WindowSize:   8,
		InterSendGap: 15 * time.Millisecond, // paces sends to respect the <64kbps cap
		NackTimeout:  700 * time.Millisecond,
		MaxRounds:    30,
	}
	log.Printf("field-client: sending %s transfer media_id=%d (%d bytes)", label, mediaID, len(payload))
	if err := mediaSender.Send(payload); err != nil {
		log.Printf("field-client: %s transfer media_id=%d failed: %v", label, mediaID, err)
		return
	}
	log.Printf("field-client: %s transfer media_id=%d completed", label, mediaID)
}
