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

var (
	ErrSessionNotFound = errors.New("session: not found")
	// ErrPatientLocked is returned by GetOrCreate when a different worker
	// already holds an active session for the same patient — two field
	// workers dispatched to the same location must not both stream vitals
	// into separate sessions for one patient record.
	ErrPatientLocked = errors.New("session: patient already has an active session with another worker")
)

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
	// active maps (workerID, patientID) -> token for sessions that haven't
	// ended yet, so a retried SESSION_INIT (e.g. after a lost SESSION_ACK)
	// can be recognized and answered with the existing session instead of
	// minting a duplicate.
	active map[[2]uint32]uint32
	// patientLock maps patientID -> the workerID currently holding an
	// active session for that patient, so a second worker dispatched to
	// the same patient is rejected rather than silently creating a second,
	// conflicting session.
	patientLock map[uint32]uint32
	rng         *rand.Rand
}

func NewManager() *Manager {
	return &Manager{
		sessions:    make(map[uint32]*Session),
		active:      make(map[[2]uint32]uint32),
		patientLock: make(map[uint32]uint32),
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// GetOrCreate returns the existing active session for (workerID, patientID)
// if one already exists — so a repeat SESSION_INIT for a pair that already
// has an active session resends the same session rather than creating a
// duplicate — or allocates a new session otherwise. reused reports whether
// an existing session was returned. If patientID already has an active
// session under a *different* workerID, it returns ErrPatientLocked instead
// of creating a conflicting second session.
func (m *Manager) GetOrCreate(workerID, patientID uint32, addr *net.UDPAddr) (s *Session, reused bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := [2]uint32{workerID, patientID}
	if token, ok := m.active[key]; ok {
		if existing, ok := m.sessions[token]; ok {
			existing.FieldAddr = addr // client may be retrying from a new local port
			return existing, true, nil
		}
		delete(m.active, key)
	}

	if lockedBy, ok := m.patientLock[patientID]; ok && lockedBy != workerID {
		return nil, false, ErrPatientLocked
	}

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

	s = &Session{
		Token:     token,
		WorkerID:  workerID,
		PatientID: patientID,
		FieldAddr: addr,
		CreatedAt: time.Now(),
	}
	m.sessions[token] = s
	m.active[key] = token
	m.patientLock[patientID] = workerID
	return s, false, nil
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
	if s, ok := m.sessions[token]; ok {
		delete(m.active, [2]uint32{s.WorkerID, s.PatientID})
		if m.patientLock[s.PatientID] == s.WorkerID {
			delete(m.patientLock, s.PatientID)
		}
	}
	delete(m.sessions, token)
}

// ---- Persistence across server restarts ----
//
// Session state lives in memory; a server crash/restart would otherwise
// silently drop every active field session. Export/Import let the caller
// (cmd/server) periodically snapshot this state to disk and restore it on
// startup — a field client's retried SESSION_INIT (already deduped via
// GetOrCreate above) then resumes the same session/token instead of the
// server treating it as brand new.

// PersistedSession is the on-disk representation of one active Session.
type PersistedSession struct {
	Token       uint32    `json:"token"`
	WorkerID    uint32    `json:"worker_id"`
	PatientID   uint32    `json:"patient_id"`
	FieldAddr   string    `json:"field_addr"`
	CreatedAt   time.Time `json:"created_at"`
	DoctorReady bool      `json:"doctor_ready"`
}

// Export returns a serializable snapshot of every active session.
func (m *Manager) Export() []PersistedSession {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]PersistedSession, 0, len(m.sessions))
	for _, s := range m.sessions {
		addr := ""
		if s.FieldAddr != nil {
			addr = s.FieldAddr.String()
		}
		out = append(out, PersistedSession{
			Token:       s.Token,
			WorkerID:    s.WorkerID,
			PatientID:   s.PatientID,
			FieldAddr:   addr,
			CreatedAt:   s.CreatedAt,
			DoctorReady: s.DoctorReady,
		})
	}
	return out
}

// Import restores sessions from a snapshot produced by Export. It's meant
// to be called once at startup, before the UDP listener starts accepting
// packets. Entries with an unparseable FieldAddr are skipped rather than
// failing the whole restore.
func (m *Manager) Import(sessions []PersistedSession) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, ps := range sessions {
		addr, err := net.ResolveUDPAddr("udp", ps.FieldAddr)
		if err != nil {
			continue
		}
		s := &Session{
			Token:       ps.Token,
			WorkerID:    ps.WorkerID,
			PatientID:   ps.PatientID,
			FieldAddr:   addr,
			CreatedAt:   ps.CreatedAt,
			DoctorReady: ps.DoctorReady,
		}
		m.sessions[s.Token] = s
		m.active[[2]uint32{s.WorkerID, s.PatientID}] = s.Token
		m.patientLock[s.PatientID] = s.WorkerID
	}
}
