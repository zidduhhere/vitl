// Command server is the VitalLink gateway: it terminates the constrained
// UDP link from field clients, brokers sessions, looks up EHR records, and
// bridges everything to connected doctor dashboards over WebSocket.
package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zidduhhere/vitl/internal/ehr"
	"github.com/zidduhhere/vitl/internal/media"
	"github.com/zidduhhere/vitl/internal/security"
	"github.com/zidduhhere/vitl/internal/session"
	"github.com/zidduhhere/vitl/internal/transport"
)

// persistInterval governs how often session/dashboard state is snapshotted
// to disk so a server restart doesn't silently drop active field sessions.
const persistInterval = 3 * time.Second

func main() {
	udpAddr := flag.String("udp", ":9000", "UDP listen address for field clients")
	wsAddr := flag.String("ws", ":8080", "HTTP/WebSocket listen address for the doctor dashboard")
	dbPath := flag.String("db", "./vitallink.db", "path to the SQLite EHR database")
	statePath := flag.String("state-file", "./vitallink_state.json", "path to persist session/dashboard state across restarts")
	psk := flag.String("psk", envOr("VITALLINK_PSK", "vitallink-demo-psk-change-me"), "pre-shared key securing the field<->server UDP link (also settable via VITALLINK_PSK)")
	flag.Parse()

	securityKey := security.DeriveKey(*psk)

	store, err := ehr.Open(*dbPath)
	if err != nil {
		log.Fatalf("server: failed to open EHR store: %v", err)
	}
	defer store.Close()

	hub := NewHub()

	udpConnAddr, err := net.ResolveUDPAddr("udp", *udpAddr)
	if err != nil {
		log.Fatalf("server: bad UDP address %q: %v", *udpAddr, err)
	}
	udpConn, err := net.ListenUDP("udp", udpConnAddr)
	if err != nil {
		log.Fatalf("server: failed to listen on UDP %s: %v", *udpAddr, err)
	}
	defer udpConn.Close()

	srv := &server{
		udpConn:       udpConn,
		store:         store,
		sessions:      session.NewManager(),
		hub:           hub,
		reassembler:   media.NewReassembler(),
		dedup:         transport.NewSeqDedup(64),
		kicks:         make(map[uint64]chan struct{}),
		lastEHR:       make(map[uint32]wsEHRPush),
		sessionStatus: make(map[uint32]wsSessionStatus),
		lastVitals:    make(map[uint32]wsVitals),
		securityKey:   securityKey,
	}
	hub.OnMessage = srv.handleDashboardMessage
	hub.Snapshot = srv.snapshot

	if d, ok := loadStateFile(*statePath); ok {
		srv.importDiskState(d)
		log.Printf("server: restored %d session(s) from %s", len(d.Sessions), *statePath)
	}

	stop := make(chan struct{})
	go srv.persistLoop(*statePath, persistInterval, stop)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Printf("server: shutting down, flushing state to %s", *statePath)
		close(stop)
		time.Sleep(200 * time.Millisecond) // let persistLoop's final write land
		os.Exit(0)
	}()

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir("./dashboard")))

	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		id, err := store.AuthenticateDoctor(req.Username, req.Password)
		if err != nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		// Return a simple dummy token and the doctor's ID
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token":     "authenticated-token",
			"doctor_id": id,
		})
	})

	mux.HandleFunc("/api/patients", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var p ehr.Patient
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		id, err := store.AddPatient(&p)
		if err != nil {
			log.Printf("server: failed to add patient: %v", err)
			http.Error(w, "Failed to save patient", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"patient_id": id})
	})

	mux.HandleFunc("/ws", hub.HandleWS)
	// /vitals is the baseline-client's naive HTTP target — same host, same
	// netem conditions, so its contrast against the UDP+ARQ path is fair.
	// It does no processing on purpose: the demo point is that plain
	// HTTP/TCP stalls under loss well before the response body matters.
	mux.HandleFunc("/vitals", func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})

	go func() {
		log.Printf("server: dashboard WS/HTTP listening on %s", *wsAddr)
		if err := http.ListenAndServe(*wsAddr, mux); err != nil {
			log.Fatalf("server: HTTP server failed: %v", err)
		}
	}()

	log.Printf("server: UDP field link listening on %s (encrypted)", *udpAddr)
	buf := make([]byte, 2048)
	for {
		n, addr, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("server: UDP read error: %v", err)
			continue
		}
		sealed := make([]byte, n)
		copy(sealed, buf[:n])
		data, err := security.Open(srv.securityKey, sealed)
		if err != nil {
			// Wrong/missing key, corrupted, or replayed-garbage datagram —
			// treat the same as a checksum failure: drop silently, no ACK.
			continue
		}
		go srv.handlePacket(addr, data)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
