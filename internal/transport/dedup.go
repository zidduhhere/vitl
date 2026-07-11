package transport

import "sync"

// SeqDedup tracks which (session, seq_num) pairs have already been seen so
// retransmitted VITALS packets aren't double-counted. Bounded per session
// to a sliding window of recent sequence numbers — this is a demo-scale
// implementation, not built for long-running production sessions.
type SeqDedup struct {
	mu      sync.Mutex
	window  int
	seen    map[uint32]map[uint16]struct{}
	highest map[uint32]uint16
}

func NewSeqDedup(window int) *SeqDedup {
	return &SeqDedup{
		window:  window,
		seen:    make(map[uint32]map[uint16]struct{}),
		highest: make(map[uint32]uint16),
	}
}

// Seen reports whether seq for the given session has already been recorded,
// and records it if not.
func (d *SeqDedup) Seen(sessionToken uint32, seq uint16) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	set, ok := d.seen[sessionToken]
	if !ok {
		set = make(map[uint16]struct{})
		d.seen[sessionToken] = set
	}
	if _, dup := set[seq]; dup {
		return true
	}
	set[seq] = struct{}{}
	if seq > d.highest[sessionToken] {
		d.highest[sessionToken] = seq
	}
	if len(set) > d.window {
		// Evict the oldest-looking entries relative to the highest seen.
		cutoff := d.highest[sessionToken] - uint16(d.window)
		for s := range set {
			if s < cutoff {
				delete(set, s)
			}
		}
	}
	return false
}

func (d *SeqDedup) DropSession(sessionToken uint32) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.seen, sessionToken)
	delete(d.highest, sessionToken)
}
