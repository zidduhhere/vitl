// Package transport implements the reliability layer on top of raw UDP:
// stop-and-wait ARQ for session/vitals packets (transport/udp.go) and a
// sliding-window/NACK scheme for chunked media (transport/media_window.go).
//
// Both work against a channel of inbound packets rather than reading the
// socket directly, so a single receiver goroutine can own the UDP socket
// and fan responses out to whichever sender is waiting — a field client
// has several concurrent senders (session, vitals, media) sharing one
// connected UDP socket, and only one goroutine may safely call Read on it.
package transport

import (
	"errors"
	"time"
)

var ErrMaxRetriesExceeded = errors.New("transport: max retries exceeded, no valid response")

// SendWithRetry implements stop-and-wait ARQ: call write(payload), then
// wait up to timeout on respCh for a response that satisfies validate,
// retrying (re-calling write) on timeout or on responses that don't match.
// Used for SESSION_INIT->SESSION_ACK (long timeout, many retries — must
// eventually succeed) and for VITALS->VITALS_ACK (short timeout, few
// retries — freshness over completeness, an old vitals packet isn't worth
// chasing).
func SendWithRetry(write func([]byte) error, respCh <-chan []byte, payload []byte, timeout time.Duration, maxRetries int, validate func([]byte) bool) ([]byte, error) {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := write(payload); err != nil {
			return nil, err
		}

		deadline := time.NewTimer(timeout)
		for {
			select {
			case resp := <-respCh:
				if validate == nil || validate(resp) {
					deadline.Stop()
					return resp, nil
				}
				// not the response we're waiting for — keep listening
				// within this same window
			case <-deadline.C:
				goto nextAttempt
			}
		}
	nextAttempt:
	}
	return nil, ErrMaxRetriesExceeded
}
