// Package vitallink exposes a gomobile-safe API over the VitalLink
// protocol and transport internals.  Only types that gomobile bind
// supports are exported: string, bool, signed integers (int/int64/int32),
// []byte, and exported structs/interfaces.  uint32/uint16/channels/generics
// must not appear on the exported surface — they're used internally but
// kept behind the Client facade.
//
// Usage from Kotlin (via the generated .aar):
//
//	val client = Vitallink.newClient("192.168.1.10:9000", myListener)
//	client.startSession(workerID, patientID)
//	client.sendVitals(72, 98, 120, 80, 368)   // tempX10 = 36.8°C
//	client.sendImage(jpegBytes)
//	client.endSession()
//	client.close()
package vitallink

import (
	"fmt"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zidduhhere/vitl/internal/protocol"
	"github.com/zidduhhere/vitl/internal/security"
	"github.com/zidduhhere/vitl/internal/transport"
)

// defaultPSK matches the server's default (cmd/server/main.go's -psk flag
// default). A real deployment overrides both ends via NewClientWithKey and
// the server's -psk/VITALLINK_PSK — this default only covers the
// out-of-the-box demo setup so existing Android integrations that call
// NewClient don't need to change.
const defaultPSK = "vitallink-demo-psk-change-me"

// Listener is implemented by the Android Activity (or a Kotlin wrapper).
// Callbacks arrive from the internal receive-loop goroutine, so the
// Android implementation must dispatch UI updates via runOnUiThread.
//
// gomobile bind generates a Java interface for this automatically — the
// Kotlin side creates an anonymous object implementing it and passes it
// to NewClient.
type Listener interface {
	// OnSessionStatus is called after StartSession returns.
	// status is one of "ok", "patient_not_found", "no_doctor",
	// "patient_locked", "unauthorized", "error:<msg>".
	// sessionToken is the opaque token assigned by the server (0 on error).
	OnSessionStatus(status string, sessionToken int64)

	// OnDoctorReady fires when the assigned doctor confirms they are
	// watching the session.  doctorID and message come from the server.
	OnDoctorReady(doctorID int, message string)

	// OnDoctorMsg fires when the doctor sends a coded instruction.
	// code values match protocol.MsgXxx constants (0x01–0x04).
	OnDoctorMsg(code int)

	// OnVitalsAck fires after each SendVitals call (whether ACKed or not).
	// ok=true means the server acknowledged the reading; ok=false means it
	// was dropped/unACKed (freshness-over-completeness: we move on either way).
	OnVitalsAck(seq int, ok bool)

	// OnMediaProgress fires during SendImage to report chunking progress.
	// sent is the number of chunks confirmed delivered; total is the full count.
	// When sent==total, the transfer is complete.
	OnMediaProgress(mediaID int, sent int, total int)
}

// maxOfflineBuffer bounds the offline vitals backlog (edge-cases.md: field
// device goes fully offline for a period). Bounded so a dead zone can't
// grow memory unboundedly on a phone; freshness over completeness still
// governs — once full, the oldest backlog entry is dropped, not the
// newest reading.
const maxOfflineBuffer = 60

// Client wraps a UDP connection and the VitalLink session/transport state.
// Create with NewClient; close with Close when the Activity is destroyed.
type Client struct {
	conn        *net.UDPConn
	listener    Listener
	securityKey [security.KeySize]byte

	// sessionToken is written once by StartSession, then read-only.
	sessionToken uint32
	// seqCounter increments for every outgoing VITALS packet.
	seqCounter uint16
	seqMu      sync.Mutex

	// offlineBuffer holds encoded VITALS packets that failed to ACK, so
	// they can be replayed as a backlog burst once the link is confirmed
	// back rather than being permanently lost.
	offlineMu     sync.Mutex
	offlineBuffer [][]byte

	// Channels fanned out by the receive-loop goroutine.
	sessionAckCh  chan []byte
	vitalsAckCh   chan []byte
	doctorReadyCh chan protocol.DoctorReadyPacket
	doctorMsgCh   chan protocol.DoctorMsgPacket

	// nackRegistry routes MEDIA_NACK packets to the right pending transfer.
	nackRegistry *mediaNackRegistry

	// mediaIDCounter generates unique media IDs per transfer.
	mediaIDCounter uint32

	// sender gives vitals packets priority over media chunks on the shared
	// UDP socket, so a large image/audio transfer can't crowd out a
	// moment-to-moment vitals reading (edge-cases.md #12), and seals every
	// outbound payload with AES-256-GCM under securityKey.
	sender *transport.PrioritySender

	// closed signals the receive loop to stop.
	closed chan struct{}
}

// NewClient dials the server over UDP and starts the internal receive-loop
// goroutine, securing the link with the default demo pre-shared key (see
// defaultPSK). serverAddr must be in "host:port" form (e.g. "10.0.0.1:9000").
// Call Close when done. Use NewClientWithKey for a deployment-specific key.
func NewClient(serverAddr string, listener Listener) (*Client, error) {
	return NewClientWithKey(serverAddr, defaultPSK, listener)
}

// NewClientWithKey is NewClient but with an explicit pre-shared key, for
// deployments that don't use the built-in demo default.
func NewClientWithKey(serverAddr, psk string, listener Listener) (*Client, error) {
	raddr, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		return nil, fmt.Errorf("vitallink: bad server address %q: %w", serverAddr, err)
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return nil, fmt.Errorf("vitallink: dial failed: %w", err)
	}

	key := security.DeriveKey(psk)
	c := &Client{
		conn:          conn,
		listener:      listener,
		securityKey:   key,
		sessionAckCh:  make(chan []byte, 4),
		vitalsAckCh:   make(chan []byte, 16),
		doctorReadyCh: make(chan protocol.DoctorReadyPacket, 4),
		doctorMsgCh:   make(chan protocol.DoctorMsgPacket, 16),
		nackRegistry:  newMediaNackRegistry(),
		sender:        transport.NewPrioritySender(security.SealingWriter{W: conn, Key: key}),
		closed:        make(chan struct{}),
	}

	go c.receiveLoop()
	go c.dispatchDoctorEvents()

	return c, nil
}

// StartSession sends SESSION_INIT to the server and waits for SESSION_ACK.
// The result is reported via Listener.OnSessionStatus.
// workerID and patientID are cast to uint32 on the wire.
func (c *Client) StartSession(workerID, patientID int64) error {
	pkt := protocol.SessionInitPacket{
		WorkerID:  uint32(workerID),
		PatientID: uint32(patientID),
		Timestamp: uint32(time.Now().Unix()),
		AuthToken: security.DeriveWorkerToken(c.securityKey, uint32(workerID)),
	}

	resp, err := transport.SendWithRetry(
		c.sender.VitalsWrite,
		c.sessionAckCh,
		pkt.Encode(),
		2*time.Second, 5,
		func(b []byte) bool {
			t, err := protocol.PacketType(b)
			return err == nil && t == protocol.TypeSessionAck
		},
	)
	if err != nil {
		c.listener.OnSessionStatus("error:"+err.Error(), 0)
		return err
	}

	ack, err := protocol.DecodeSessionAck(resp)
	if err != nil {
		c.listener.OnSessionStatus("error:malformed_ack", 0)
		return err
	}

	c.sessionToken = ack.SessionToken
	switch ack.StatusCode {
	case protocol.StatusPatientNotFound:
		c.listener.OnSessionStatus("patient_not_found", 0)
	case protocol.StatusUnauthorized:
		c.listener.OnSessionStatus("unauthorized", 0)
	case protocol.StatusPatientLocked:
		c.listener.OnSessionStatus("patient_locked", 0)
	case protocol.StatusNoDoctorAvailable:
		c.listener.OnSessionStatus("no_doctor", int64(ack.SessionToken))
	default:
		c.listener.OnSessionStatus("ok", int64(ack.SessionToken))
	}
	return nil
}

// SendVitals builds and sends a VITALS packet with stop-and-wait ARQ
// (short timeout, 1 retry — freshness over completeness). If the link is
// fully offline (not just lossy), the reading is buffered and replayed as
// a backlog burst once the link comes back rather than being lost.
// All parameters are plain ints to satisfy gomobile's type restrictions.
//   - heartRate: beats per minute (50–200)
//   - spo2:      SpO2 percentage (85–100)
//   - bpSystolic / bpDiastolic: mmHg
//   - tempX10:   temperature in Celsius × 10 (e.g. 368 = 36.8°C)
func (c *Client) SendVitals(heartRate, spo2, bpSystolic, bpDiastolic, tempX10 int) error {
	c.seqMu.Lock()
	seq := c.seqCounter
	c.seqCounter++
	c.seqMu.Unlock()

	pkt := protocol.VitalsPacket{
		SessionToken: c.sessionToken,
		SeqNum:       seq,
		HeartRate:    byte(heartRate),
		SpO2:         byte(spo2),
		BPSystolic:   byte(bpSystolic),
		BPDiastolic:  byte(bpDiastolic),
		Temp:         protocol.EncodeTempByte(tempX10),
		DeltaFlag:    0, // always full snapshot
		Timestamp:    uint32(time.Now().Unix()),
	}
	encoded := pkt.Encode()

	_, err := transport.SendWithRetry(
		c.sender.VitalsWrite,
		c.vitalsAckCh,
		encoded,
		400*time.Millisecond, 1,
		func(b []byte) bool {
			va, err := protocol.DecodeVitalsAck(b)
			return err == nil && va.SessionToken == c.sessionToken && va.AckSeqNum == seq
		},
	)

	if err != nil {
		c.bufferOffline(encoded)
		c.listener.OnVitalsAck(int(seq), false)
	} else {
		c.flushOfflineBuffer()
		c.listener.OnVitalsAck(int(seq), true)
	}
	return nil // always return nil — unACKed vitals are not a fatal error
}

func (c *Client) bufferOffline(encoded []byte) {
	c.offlineMu.Lock()
	defer c.offlineMu.Unlock()
	c.offlineBuffer = append(c.offlineBuffer, encoded)
	if len(c.offlineBuffer) > maxOfflineBuffer {
		c.offlineBuffer = c.offlineBuffer[1:]
	}
}

// flushOfflineBuffer resends any backlog collected while offline now that
// the link is confirmed back (an ACK just succeeded). Best-effort,
// fire-and-forget — these are already-stale readings, not worth a full
// retry cycle each.
func (c *Client) flushOfflineBuffer() {
	c.offlineMu.Lock()
	backlog := c.offlineBuffer
	c.offlineBuffer = nil
	c.offlineMu.Unlock()

	for _, encoded := range backlog {
		c.sender.VitalsWrite(encoded)
	}
}

// SendImage chunks the already-encoded JPEG bytes and streams them to the
// server using the sliding-window/NACK scheme from transport.MediaSender.
// The Kotlin side is responsible for capturing the image, downscaling to
// ~320px long-side, and JPEG-compressing before calling this (Android's
// Bitmap.compress is simpler and more idiomatic than routing through the
// server's media/image.go).
// Progress is reported via Listener.OnMediaProgress.
func (c *Client) SendImage(jpegBytes []byte) error {
	mediaID := uint16(atomic.AddUint32(&c.mediaIDCounter, 1) & 0xFFFF)
	// Avoid zero — server uses 0 as "no transfer".
	if mediaID == 0 {
		mediaID = 1
	}

	nackCh := c.nackRegistry.subscribe(mediaID)
	defer c.nackRegistry.unsubscribe(mediaID)

	// Calculate total chunks for progress reporting.
	totalChunks := (len(jpegBytes) + protocol.MaxChunkPayload - 1) / protocol.MaxChunkPayload
	if totalChunks == 0 {
		totalChunks = 1
	}

	sender := &transport.MediaSender{
		Write:        c.sender.MediaWrite,
		NackCh:       nackCh,
		SessionToken: c.sessionToken,
		MediaID:      mediaID,
		ChunkType:    protocol.TypeImageChunk,
		WindowSize:   8,
		InterSendGap: 15 * time.Millisecond, // pace to respect <64kbps cap
		NackTimeout:  700 * time.Millisecond,
		MaxRounds:    30,
	}

	// Wrap Send in a goroutine-safe progress reporter.
	// MediaSender.Send is blocking — run it and report progress at the end.
	// (For finer-grained progress, the MediaSender API would need callbacks;
	// the current implementation reports completion at end-of-transfer.)
	c.listener.OnMediaProgress(int(mediaID), 0, totalChunks)
	err := sender.Send(jpegBytes)
	if err != nil {
		c.listener.OnMediaProgress(int(mediaID), 0, totalChunks)
		return fmt.Errorf("vitallink: image transfer media_id=%d failed: %w", mediaID, err)
	}
	c.listener.OnMediaProgress(int(mediaID), totalChunks, totalChunks)
	return nil
}

// EndSession sends SESSION_END to the server.
func (c *Client) EndSession() error {
	pkt := protocol.SessionEndPacket{SessionToken: c.sessionToken}
	return c.sender.VitalsWrite(pkt.Encode())
}

// Close shuts down the receive loop and closes the UDP connection.
// Always call this when the Android Activity is destroyed.
func (c *Client) Close() {
	select {
	case <-c.closed:
		// already closed
	default:
		close(c.closed)
	}
	c.sender.Close()
	c.conn.Close()
}

// ---- Internal helpers ----

// receiveLoop owns the socket read side and fans packets out by type.
// Only one goroutine reads from the UDP conn — this avoids the concurrent-
// read hazard that was found and fixed in cmd/field-client.
func (c *Client) receiveLoop() {
	buf := make([]byte, 2048)
	for {
		select {
		case <-c.closed:
			return
		default:
		}

		c.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, err := c.conn.Read(buf)
		if err != nil {
			// Deadline exceeded is expected — just loop and check closed.
			continue
		}

		data, err := security.Open(c.securityKey, buf[:n])
		if err != nil {
			// Wrong/missing key or corrupted datagram — drop silently,
			// same treatment as a checksum failure.
			continue
		}

		t, err := protocol.PacketType(data)
		if err != nil {
			continue
		}
		switch t {
		case protocol.TypeSessionAck:
			select {
			case c.sessionAckCh <- data:
			default:
			}
		case protocol.TypeVitalsAck:
			select {
			case c.vitalsAckCh <- data:
			default:
			}
		case protocol.TypeDoctorReady:
			if p, err := protocol.DecodeDoctorReady(data); err == nil {
				select {
				case c.doctorReadyCh <- p:
				default:
				}
			}
		case protocol.TypeDoctorMsg:
			if p, err := protocol.DecodeDoctorMsg(data); err == nil {
				select {
				case c.doctorMsgCh <- p:
				default:
				}
			}
		case protocol.TypeMediaNack:
			if p, err := protocol.DecodeMediaNack(data); err == nil {
				c.nackRegistry.dispatch(p)
			}
		}
	}
}

// dispatchDoctorEvents reads doctor packets and calls listener callbacks.
// Runs in its own goroutine so it doesn't block the receive loop.
func (c *Client) dispatchDoctorEvents() {
	for {
		select {
		case <-c.closed:
			return
		case dr := <-c.doctorReadyCh:
			c.listener.OnDoctorReady(int(dr.DoctorID), dr.Message)
		case dm := <-c.doctorMsgCh:
			c.listener.OnDoctorMsg(int(dm.Code))
		}
	}
}

// ---- mediaNackRegistry ----
// Routes MEDIA_NACK packets to the specific pending transfer by media_id.
// Identical to the one in cmd/field-client — extracted here rather than
// shared from an internal package to keep the mobile package self-contained
// and avoid pulling cmd/ into the bind graph.

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

// Ensure rand is imported (used by mediaID fallback seed in tests).
var _ = rand.New
