# VitalLink — Implementation Status

Last updated: 2026-07-11

## Done

| Phase (per implementation_plan.md) | Component | State |
|---|---|---|
| 1 | `internal/protocol` | Done. All 11 packet types (SESSION_INIT/ACK, DOCTOR_READY, VITALS/ACK, DOCTOR_MSG, HEARTBEAT, SESSION_END, AUDIO/IMAGE_CHUNK, MEDIA_NACK) with Encode/Decode + checksum. |
| 2–3 | `internal/transport` | Done. `SendWithRetry` (stop-and-wait ARQ, channel-based so it composes with a shared UDP receive loop), `SeqDedup` (per-session sliding window dedup), `MediaSender` (sliding-window + NACK-driven retransmit). |
| 4 | `internal/session` | Done. In-memory token-keyed session map, random collision-free token generation. |
| 4 | `internal/ehr` | Done. SQLite (`modernc.org/sqlite`, pure Go, no cgo) via `go:embed`'d `seed.sql`, 5 dummy patients (ids 1001–1005). |
| 6/9 | `internal/media` | Done. `audio.go` shells out to `opusenc`, `image.go` does stdlib-only JPEG downscale/encode, `reassembly.go` is the server-side chunk bitmap + multiplexed `Reassembler`. |
| 5, 7 | `cmd/server` | Done. UDP listener (session handshake, vitals ACK+relay, media reassembly+NACK, session end), WebSocket hub broadcasting JSON to dashboards, `/vitals` HTTP endpoint for the baseline client, doctor→field relay (DOCTOR_READY/DOCTOR_MSG). |
| — | `cmd/field-client` | Done. Session handshake with retry, simulated vitals random-walk loop (freshness-over-completeness ARQ), optional `-audio-file`/`-image-file` chunked transfer, graceful SIGINT → SESSION_END. |
| 7 | `cmd/baseline-client` | Done. Naive HTTP POST loop, no retry/reliability — the demo contrast case. |
| 8 | `network-sim/` | Done. `apply_netem.sh` / `reset_netem.sh` (Linux `tc qdisc netem`, configurable loss/rate/delay). |
| — | `go.mod` / `go.sum` | Done. Added `github.com/gorilla/websocket`, `modernc.org/sqlite`. |

**Verified by running it** (not just `go build`): full session handshake, 14/14 vitals ACKed and relayed to a WS client, one clean audio transfer (opusenc → chunk → sliding-window send → server reassembly → single WS broadcast, no duplicates), patient-not-found rejection path, baseline HTTP client success path.

## Explicitly left out (per instruction)

- **`dashboard/`** — UI/JS/CSS. `dashboard/index.html` exists from a prior commit and was not touched. There is no `app.js`/`style.css` wiring it to the server's `/ws` endpoint yet — the server-side hub and JSON message contract (`cmd/server/messages.go`) are ready for it (`ehr_push`, `vitals`, `media`, `session_status` message types).

## Bugs found & fixed during implementation

1. **Temp field byte overflow.** Spec says VITALS `temp` is 1 byte, "x10 for one decimal" (e.g. 368 for 36.8°C) — that doesn't fit in a byte and silently truncated (36.8°C displayed as 11.1°C). Fixed with an offset encoding (`protocol.EncodeTempByte`/`DecodeTempByte`, offset 300, covering 30.0–55.5°C in one byte).
2. **Duplicate media broadcast.** Original design had both the chunk handler and a periodic watcher goroutine independently able to detect "transfer complete" and broadcast to the dashboard, racing under load. Refactored to a single event-driven watcher goroutine per transfer (mediaID-keyed, kicked on completion, ticked otherwise) that is the sole owner of NACK-sending and completion broadcast.
3. **Concurrent-read hazard in field-client.** Initial ARQ design had each sender (session, vitals, media) call `conn.Read` directly — unsafe with multiple concurrent senders on one UDP socket. Reworked into one receive loop that fans packets out to per-type/per-transfer channels; `transport.SendWithRetry` and `MediaSender` now consume from a channel instead of reading the socket themselves.

## Not yet done / stretch scope

- **Phase 9.5 (polish, optional per plan):** delta-encoding flag logic (`delta_flag` is wired into the packet but always sent as `0`/full snapshot — no actual diffing logic), `DOCTOR_MSG` sending from the dashboard side (server-side relay exists, but nothing sends it without a dashboard UI).
- **Phase 10 (demo prep):** not applicable to code implementation — backup video, pitch script.
- **Dashboard UI** (see above — explicitly out of scope).
- No automated test suite yet (`go test ./...` has no tests); verification so far has been live end-to-end runs, not unit tests.
