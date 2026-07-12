package main

import (
	"encoding/json"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/zidduhhere/vitl/internal/session"
)

// diskState is the on-disk snapshot of everything needed to resume active
// sessions and the dashboard's view of them after a server restart. Media
// transfers in flight and the dedup window are deliberately not persisted —
// worst case after a restart is one re-processed vitals reading or a
// media transfer that has to restart, which the existing retry/ARQ paths
// already handle.
type diskState struct {
	Sessions      []session.PersistedSession `json:"sessions"`
	LastEHR       map[string]wsEHRPush       `json:"last_ehr"`
	SessionStatus map[string]wsSessionStatus `json:"session_status"`
	LastVitals    map[string]wsVitals        `json:"last_vitals"`
}

func (s *server) exportDiskState() diskState {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()

	d := diskState{
		Sessions:      s.sessions.Export(),
		LastEHR:       make(map[string]wsEHRPush, len(s.lastEHR)),
		SessionStatus: make(map[string]wsSessionStatus, len(s.sessionStatus)),
		LastVitals:    make(map[string]wsVitals, len(s.lastVitals)),
	}
	for token, v := range s.lastEHR {
		d.LastEHR[strconv.FormatUint(uint64(token), 10)] = v
	}
	for token, v := range s.sessionStatus {
		d.SessionStatus[strconv.FormatUint(uint64(token), 10)] = v
	}
	for token, v := range s.lastVitals {
		d.LastVitals[strconv.FormatUint(uint64(token), 10)] = v
	}
	return d
}

// importDiskState restores session and dashboard-cache state from a
// previous run. Call once at startup, before the UDP listener starts
// accepting packets.
func (s *server) importDiskState(d diskState) {
	s.sessions.Import(d.Sessions)

	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	for tokenStr, v := range d.LastEHR {
		if token, err := strconv.ParseUint(tokenStr, 10, 32); err == nil {
			s.lastEHR[uint32(token)] = v
		}
	}
	for tokenStr, v := range d.SessionStatus {
		if token, err := strconv.ParseUint(tokenStr, 10, 32); err == nil {
			s.sessionStatus[uint32(token)] = v
		}
	}
	for tokenStr, v := range d.LastVitals {
		if token, err := strconv.ParseUint(tokenStr, 10, 32); err == nil {
			s.lastVitals[uint32(token)] = v
		}
	}
}

// writeStateFile marshals the current state and writes it atomically
// (temp file + rename) so a crash mid-write never leaves a corrupt file
// behind for the next startup to choke on.
func (s *server) writeStateFile(path string) {
	data, err := json.MarshalIndent(s.exportDiskState(), "", "  ")
	if err != nil {
		log.Printf("server: failed to marshal state: %v", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("server: failed to write state file: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("server: failed to finalize state file: %v", err)
	}
}

func loadStateFile(path string) (diskState, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return diskState{}, false
	}
	var d diskState
	if err := json.Unmarshal(data, &d); err != nil {
		log.Printf("server: ignoring corrupt state file %s: %v", path, err)
		return diskState{}, false
	}
	return d, true
}

// persistLoop periodically snapshots state to disk, and writes one final
// snapshot when stop is closed (graceful shutdown) so the most recent
// state is never more than persistInterval stale.
func (s *server) persistLoop(path string, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			s.writeStateFile(path)
			return
		case <-ticker.C:
			s.writeStateFile(path)
		}
	}
}
