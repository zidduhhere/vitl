// Package session manages in-memory session state: token generation and
// the mapping from session token to the field worker/patient context and
// UDP address that owns it.
package session

import (
	"errors"
	"math/rand"
	"net"
	"sync"
	"time"
)

var ErrSessionNotFound = errors.New("session: not found")

type Session struct {
	Token       uint32
	WorkerID    uint32
	PatientID   uint32
	FieldAddr   *net.UDPAddr
	CreatedAt   time.Time
	DoctorReady bool
}

type Manager struct {
	mu       sync.Mutex
	sessions map[uint32]*Session
	rng      *rand.Rand
}

func NewManager() *Manager {
	return &Manager{
		sessions: make(map[uint32]*Session),
		rng:      rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Create allocates a new session with a random non-zero, collision-free token.
func (m *Manager) Create(workerID, patientID uint32, addr *net.UDPAddr) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	var token uint32
	for {
		token = m.rng.Uint32()
		if token == 0 {
			continue
		}
		if _, exists := m.sessions[token]; !exists {
			break
		}
	}

	s := &Session{
		Token:     token,
		WorkerID:  workerID,
		PatientID: patientID,
		FieldAddr: addr,
		CreatedAt: time.Now(),
	}
	m.sessions[token] = s
	return s
}

func (m *Manager) Get(token uint32) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	return s, ok
}

func (m *Manager) SetDoctorReady(token uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	if !ok {
		return ErrSessionNotFound
	}
	s.DoctorReady = true
	return nil
}

func (m *Manager) End(token uint32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, token)
}
