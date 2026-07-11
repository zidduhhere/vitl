package media

import (
	"sync"
)

// Transfer tracks the reassembly state of a single media_id — a bitmap of
// which chunk indices have arrived plus the chunk bytes themselves.
type Transfer struct {
	mu         sync.Mutex
	total      uint16
	received   map[uint16][]byte
	SessionTok uint32
	MediaID    uint16
	ChunkType  byte
}

func NewTransfer(sessionToken uint32, mediaID uint16, chunkType byte, total uint16) *Transfer {
	return &Transfer{
		total:      total,
		received:   make(map[uint16][]byte, total),
		SessionTok: sessionToken,
		MediaID:    mediaID,
		ChunkType:  chunkType,
	}
}

// AddChunk records a chunk's payload and reports whether the transfer is
// now complete.
func (t *Transfer) AddChunk(idx uint16, payload []byte) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.received[idx]; !ok {
		buf := make([]byte, len(payload))
		copy(buf, payload)
		t.received[idx] = buf
	}
	return uint16(len(t.received)) >= t.total
}

// MissingIndices returns the chunk indices not yet received, in order.
func (t *Transfer) MissingIndices() []uint16 {
	t.mu.Lock()
	defer t.mu.Unlock()
	var missing []uint16
	for i := uint16(0); i < t.total; i++ {
		if _, ok := t.received[i]; !ok {
			missing = append(missing, i)
		}
	}
	return missing
}

// Assemble concatenates all chunks in order. Only valid once complete.
func (t *Transfer) Assemble() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []byte
	for i := uint16(0); i < t.total; i++ {
		out = append(out, t.received[i]...)
	}
	return out
}

// Reassembler multiplexes reassembly state across concurrent transfers,
// keyed by (session_token, media_id).
type Reassembler struct {
	mu        sync.Mutex
	transfers map[uint64]*Transfer
}

func NewReassembler() *Reassembler {
	return &Reassembler{transfers: make(map[uint64]*Transfer)}
}

func key(sessionToken uint32, mediaID uint16) uint64 {
	return uint64(sessionToken)<<16 | uint64(mediaID)
}

// GetOrCreate returns the Transfer for this (session, media) pair,
// creating it on first sight of a chunk.
func (r *Reassembler) GetOrCreate(sessionToken uint32, mediaID uint16, chunkType byte, total uint16) *Transfer {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := key(sessionToken, mediaID)
	t, ok := r.transfers[k]
	if !ok {
		t = NewTransfer(sessionToken, mediaID, chunkType, total)
		r.transfers[k] = t
	}
	return t
}

func (r *Reassembler) Drop(sessionToken uint32, mediaID uint16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.transfers, key(sessionToken, mediaID))
}
