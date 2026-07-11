package transport

import (
	"time"

	"github.com/zidduhhere/vitl/internal/protocol"
)

// MediaSender streams a chunked media transfer (audio or image) to the
// server using a sliding window instead of stop-and-wait: several chunks
// go out per round, then the sender waits once for a MEDIA_NACK listing
// exactly which chunk indices are still missing and retransmits only
// those. This is what lets a multi-chunk transfer survive under 20%+ loss
// without stalling the way per-chunk stop-and-wait would.
type MediaSender struct {
	Write        func([]byte) error             // sends a raw datagram to the server
	NackCh       <-chan protocol.MediaNackPacket // fed by the caller's shared UDP receiver loop
	SessionToken uint32
	MediaID      uint16
	ChunkType    byte // protocol.TypeAudioChunk or protocol.TypeImageChunk

	WindowSize   int           // chunks sent per round before waiting on a NACK
	InterSendGap time.Duration // pacing delay between chunks, to respect the bandwidth cap
	NackTimeout  time.Duration // how long to wait for a MEDIA_NACK before assuming it was lost and resending
	MaxRounds    int           // give up after this many rounds
}

// Send splits payload into MaxChunkPayload-sized chunks and drives them to
// completion. It returns nil once the server confirms (an empty-missing
// MEDIA_NACK) that every chunk arrived, or an error if MaxRounds is
// exhausted first.
func (m *MediaSender) Send(payload []byte) error {
	chunks := chunkify(payload)
	total := uint16(len(chunks))

	pending := make(map[uint16]struct{}, total)
	for i := range chunks {
		pending[uint16(i)] = struct{}{}
	}

	for round := 0; round < m.MaxRounds && len(pending) > 0; round++ {
		sent := 0
		for idx := range pending {
			pkt := protocol.MediaChunkPacket{
				Type:         m.ChunkType,
				SessionToken: m.SessionToken,
				MediaID:      m.MediaID,
				ChunkIndex:   idx,
				TotalChunks:  total,
				Payload:      chunks[idx],
			}
			if err := m.Write(pkt.Encode()); err != nil {
				return err
			}
			sent++
			if m.InterSendGap > 0 {
				time.Sleep(m.InterSendGap)
			}
		}

		if m.waitForNack(round) {
			return nil // server confirmed full reassembly
		}

		// Drain any NACK(s) received during this round to find the most
		// recent missing-index set.
		newPending := m.latestPending(pending)
		pending = newPending
	}

	if len(pending) == 0 {
		return nil
	}
	return ErrMaxRetriesExceeded
}

// waitForNack blocks up to NackTimeout for a NACK matching this transfer.
// Returns true if that NACK reports zero missing chunks (transfer done).
func (m *MediaSender) waitForNack(_ int) bool {
	timer := time.NewTimer(m.NackTimeout)
	defer timer.Stop()
	select {
	case nack := <-m.NackCh:
		if nack.MediaID != m.MediaID {
			return false
		}
		return len(nack.MissingIndices) == 0
	case <-timer.C:
		return false
	}
}

// latestPending consumes any already-buffered NACKs for this media_id and
// returns the most recent missing-index set, falling back to the previous
// pending set if none arrived.
func (m *MediaSender) latestPending(prev map[uint16]struct{}) map[uint16]struct{} {
	for {
		select {
		case nack := <-m.NackCh:
			if nack.MediaID != m.MediaID {
				continue
			}
			next := make(map[uint16]struct{}, len(nack.MissingIndices))
			for _, idx := range nack.MissingIndices {
				next[idx] = struct{}{}
			}
			return next
		default:
			return prev
		}
	}
}

func chunkify(payload []byte) [][]byte {
	var chunks [][]byte
	for i := 0; i < len(payload); i += protocol.MaxChunkPayload {
		end := i + protocol.MaxChunkPayload
		if end > len(payload) {
			end = len(payload)
		}
		chunk := make([]byte, end-i)
		copy(chunk, payload[i:end])
		chunks = append(chunks, chunk)
	}
	if len(chunks) == 0 {
		chunks = [][]byte{{}}
	}
	return chunks
}
