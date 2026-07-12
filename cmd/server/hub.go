package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// Hub is the Server <-> Doctor bridge. This link runs on standard clinic
// connectivity, so a plain broadcast-to-all-connected-dashboards model is
// enough — no reliability engineering needed here, that's all spent on the
// field <-> server link.
type Hub struct {
	upgrader websocket.Upgrader

	mu      sync.Mutex
	clients map[*websocket.Conn]struct{}

	// OnMessage is invoked for every JSON message received from a
	// connected dashboard (e.g. doctor_ready, doctor_msg).
	OnMessage func(wsIncoming)

	// Snapshot, if set, is called for every newly-connected dashboard to
	// get the current state of all active sessions (EHR push, session
	// status, latest vitals) so a reconnect (auto-retry after a dropped
	// connection) doesn't leave the dashboard blank until the next live
	// event.
	Snapshot func() []interface{}
}

func NewHub() *Hub {
	return &Hub{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true }, // demo-scope: no CORS lockdown
		},
		clients: make(map[*websocket.Conn]struct{}),
	}
}

func (h *Hub) HasClients() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients) > 0
}

func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("hub: upgrade failed: %v", err)
		return
	}

	h.mu.Lock()
	h.clients[conn] = struct{}{}
	h.mu.Unlock()
	log.Printf("hub: dashboard connected (%s)", conn.RemoteAddr())

	if h.Snapshot != nil {
		for _, v := range h.Snapshot() {
			raw, err := json.Marshal(v)
			if err != nil {
				continue
			}
			if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
				log.Printf("hub: snapshot write failed: %v", err)
				break
			}
		}
	}

	defer func() {
		h.mu.Lock()
		delete(h.clients, conn)
		h.mu.Unlock()
		conn.Close()
		log.Printf("hub: dashboard disconnected (%s)", conn.RemoteAddr())
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg wsIncoming
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("hub: bad message from dashboard: %v", err)
			continue
		}
		if h.OnMessage != nil {
			h.OnMessage(msg)
		}
	}
}

// Broadcast marshals v to JSON and sends it to every connected dashboard.
func (h *Hub) Broadcast(v interface{}) {
	raw, err := json.Marshal(v)
	if err != nil {
		log.Printf("hub: marshal failed: %v", err)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for conn := range h.clients {
		if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
			log.Printf("hub: write failed, dropping client: %v", err)
			conn.Close()
			delete(h.clients, conn)
		}
	}
}
