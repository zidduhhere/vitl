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

func main() {
	serverAddr := flag.String("server", "127.0.0.1:9000", "server UDP address")
	workerID := flag.Uint("worker-id", 1, "field worker/device id")
	patientID := flag.Uint("patient-id", 1001, "patient id to open a session for")
	vitalsInterval := flag.Duration("vitals-interval", 2*time.Second, "delay between VITALS packets")
	audioFile := flag.String("audio-file", "", "optional WAV file to encode+send as a chunked AUDIO_CHUNK transfer")
	imageFile := flag.String("image-file", "", "optional JPEG/PNG file to encode+send as a chunked IMAGE_CHUNK transfer")
	flag.Parse()

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
	go receiveLoop(conn, sessionAckCh, vitalsAckCh, doctorReadyCh, doctorMsgCh, nackRegistry)

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
	}
	resp, err := transport.SendWithRetry(
		func(b []byte) error { _, err := conn.Write(b); return err },
		sessionAckCh,
		initPkt.Encode(),
		2*time.Second, 5,
		func(b []byte) bool { t, err := protocol.PacketType(b); return err == nil && t == protocol.TypeSessionAck },
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
	case protocol.StatusNoDoctorAvailable:
		log.Printf("field-client: session %d established, but no doctor is connected yet", ack.SessionToken)
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
		conn.Write(end.Encode())
		close(stopVitals)
		time.Sleep(200 * time.Millisecond)
		os.Exit(0)
	}()

	// ---- Optional media transfer, sent once after the handshake ----
	if *audioFile != "" {
		go sendAudio(conn, nackRegistry, sessionToken, *audioFile)
	}
	if *imageFile != "" {
		go sendImage(conn, nackRegistry, sessionToken, *imageFile)
	}

	// ---- Steady-state vitals loop ----
	runVitalsLoop(conn, vitalsAckCh, sessionToken, *vitalsInterval, stopVitals)
}

func receiveLoop(conn *net.UDPConn, sessionAckCh, vitalsAckCh chan []byte, doctorReadyCh chan protocol.DoctorReadyPacket, doctorMsgCh chan protocol.DoctorMsgPacket, nackRegistry *mediaNackRegistry) {
	buf := make([]byte, 2048)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			continue
		}
		data := make([]byte, n)
		copy(data, buf[:n])

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

// runVitalsLoop simulates a patient's vitals with a small random walk and
// streams them. Freshness over completeness: short timeout, few retries —
// an ACK that never arrives just means we move on to the next reading
// rather than chasing a stale one.
func runVitalsLoop(conn *net.UDPConn, vitalsAckCh chan []byte, sessionToken uint32, interval time.Duration, stop <-chan struct{}) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	hr, spo2, sys, dia, tempX10 := 78, 97, 118, 76, 368 // tempX10 is Celsius x10 (36.8C)

	var seq uint16
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

			_, err := transport.SendWithRetry(
				func(b []byte) error { _, err := conn.Write(b); return err },
				vitalsAckCh,
				pkt.Encode(),
				400*time.Millisecond, 1,
				func(b []byte) bool {
					va, err := protocol.DecodeVitalsAck(b)
					return err == nil && va.SessionToken == sessionToken && va.AckSeqNum == pkt.SeqNum
				},
			)
			if err != nil {
				log.Printf("field-client: vitals seq=%d unacked, moving on (freshness over completeness)", pkt.SeqNum)
			} else {
				log.Printf("field-client: vitals seq=%d acked (hr=%d spo2=%d bp=%d/%d temp=%.1f)", pkt.SeqNum, hr, spo2, sys, dia, float64(tempX10)/10)
			}
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

func sendAudio(conn *net.UDPConn, nackRegistry *mediaNackRegistry, sessionToken uint32, path string) {
	encoded, err := media.EncodeAudioOpus(path, 16)
	if err != nil {
		log.Printf("field-client: audio encode failed: %v", err)
		return
	}
	sendMedia(conn, nackRegistry, sessionToken, protocol.TypeAudioChunk, encoded, "audio")
}

func sendImage(conn *net.UDPConn, nackRegistry *mediaNackRegistry, sessionToken uint32, path string) {
	encoded, err := media.EncodeImageJPEG(path, 320, 40)
	if err != nil {
		log.Printf("field-client: image encode failed: %v", err)
		return
	}
	sendMedia(conn, nackRegistry, sessionToken, protocol.TypeImageChunk, encoded, "image")
}

func sendMedia(conn *net.UDPConn, nackRegistry *mediaNackRegistry, sessionToken uint32, chunkType byte, payload []byte, label string) {
	mediaID := uint16(rand.New(rand.NewSource(time.Now().UnixNano())).Intn(65536))
	nackCh := nackRegistry.subscribe(mediaID)
	defer nackRegistry.unsubscribe(mediaID)

	sender := &transport.MediaSender{
		Write:        func(b []byte) error { _, err := conn.Write(b); return err },
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
	if err := sender.Send(payload); err != nil {
		log.Printf("field-client: %s transfer media_id=%d failed: %v", label, mediaID, err)
		return
	}
	log.Printf("field-client: %s transfer media_id=%d completed", label, mediaID)
}
